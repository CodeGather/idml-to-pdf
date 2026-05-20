package main

import (
	"bytes"
	"encoding/base64"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	"idml-to-pdf/internal/assets"
	"idml-to-pdf/internal/parser"
	"idml-to-pdf/internal/renderer"

)

func main() {
	var (
		idmlPath    = flag.String("idml", "", "输入的 .idml 文件路径")
		outputPath  = flag.String("out", "output.pdf", "输出的 PDF 文件路径")
		assetRoot   = flag.String("assets", "", "素材根目录（逗号分隔）")
		keepExtract = flag.Bool("keep", false, "保留解压后的临时文件")
	)
	flag.Parse()

	if *idmlPath == "" {
		fmt.Fprintln(os.Stderr, "用法: go run main.go -idml <file.idml> [-out output.pdf] [-assets ./assets]")
		os.Exit(1)
	}

	// 1. 解析 IDML
	log.Println("[1/4] 正在解析 IDML 文件...")
	doc, err := parser.OpenIDML(*idmlPath, "", *keepExtract)
	if err != nil {
		log.Fatalf("解析 IDML 失败: %v", err)
	}
	defer doc.Close()
	log.Printf("解析完成: %d 个 Spread, %d 个 Story, %d 个嵌入素材",
		len(doc.Spreads), len(doc.Stories), len(doc.EmbeddedGraphics))

	// 2. 准备素材加载器
	log.Println("[2/4] 正在准备素材加载器...")
	var roots []string
	if *assetRoot != "" {
		roots = strings.Split(*assetRoot, ",")
	}
	loader := assets.NewLoader(roots)
	loader.Logger = log.Default()
	loader.PlaceholderGenerator = simplePlaceholderGenerator

	// 3. 渲染 PDF
	log.Println("[3/4] 正在渲染 PDF...")
	rend := renderer.NewPDFRenderer()
	rend.ColorMap = doc.ColorMap
	rend.Start()

	for _, sp := range doc.Spreads {
		// 取第一个 Page 的尺寸作为 PDF 页面尺寸
		var pageW, pageH float64
		if len(sp.Pages()) > 0 {
			_, _, w, h, err := parser.GetPageBounds(sp.Pages()[0])
			if err == nil {
				pageW, pageH = w, h
			}
		}
		if pageW == 0 || pageH == 0 {
			pageW, pageH = 841.89, 595.28 // 横向 A4 默认
		}
		rend.SetPageSize(pageW, pageH)
		rend.AddPage()

		// 计算 Page 在 Spread 中的偏移
		var pageOffsetX, pageOffsetY float64
		if len(sp.Pages()) > 0 {
			px, py, _, _, err := parser.GetPageBounds(sp.Pages()[0])
			if err == nil {
				pageOffsetX, pageOffsetY = px, py
			}
		}

		items := parser.FlattenPageItems(sp)
		parser.SortItemsByLayer(items, doc)
		for _, it := range items {
			if it.Visible == "false" {
				continue
			}

			gx1, gy1, gx2, gy2, err := parser.ComputeItemGlobalBounds(it)
			if err != nil {
				log.Printf("跳过无边界元素 %s: %v", it.Self, err)
				continue
			}
			w := gx2 - gx1
			h := gy2 - gy1

			// 转换为页面相对坐标
			x := gx1 - pageOffsetX
			y := gy1 - pageOffsetY

			if x < -pageW || y < -pageH || x > pageW*2 || y > pageH*2 {
				// 过滤明显超出页面的元素
				continue
			}

		// 计算旋转角度（默认使用父级变换）
		// gopdf 内部做 Y 轴翻转 (cy = pageHeight - y)，然后使用标准 PDF 旋转矩阵 [cos sin -sin cos]。
		// 正角度在 gopdf 中 → PDF 中逆时针 → 视觉上顺时针（Y 翻转后）。
		// IDML 的 atan2(m12,m11) 正角 = 顺时针（Y-down），与 gopdf 正角视觉效果一致，故无需取反。
		m, _ := parser.ParseItemTransform(it.ItemTransform)
		angleDeg := m.ExtractRotation() * 180 / math.Pi

		// 处理图片/PDF 元素
		var uri string
		if it.Image != nil && it.Image.Link != nil {
			uri = it.Image.Link.LinkResourceURI
		}
		if it.PDF != nil && it.PDF.Link != nil {
			uri = it.PDF.Link.LinkResourceURI
		}
		if uri != "" {
			// 合并父级和子级变换，重新计算图片的全局边界和旋转角度
			igx1, igy1, igx2, igy2, _, _, imgAngle, err := parser.ComputeImageGlobalBounds(it)
		if err != nil {
			log.Printf("计算图片边界失败 %s: %v", it.Self, err)
			// 回退到标准边界
			igx1, igy1, igx2 = gx1, gy1, gx2
			parentM, _ := parser.ParseItemTransform(it.ItemTransform)
			imgAngle = parentM.ExtractRotation()
		}
			ix := igx1 - pageOffsetX
			iy := igy1 - pageOffsetY
			iw := igx2 - igx1
			ih := igy2 - igy1
			imgAngleDeg := imgAngle * 180 / math.Pi
			// 检测是否为 90°/270° 旋转（底图）
			angNorm := math.Mod(imgAngleDeg, 360)
			if angNorm < 0 {
				angNorm += 360
			}
			isRotated90 := (angNorm > 85 && angNorm < 95) || (angNorm > 265 && angNorm < 275)

			res := loader.Resolve(uri, doc.EmbeddedGraphics)
			if res.Err != nil {
				log.Printf("素材缺失: %s", filepath.Base(uri))
				rend.DrawPlaceholder(ix, iy, iw, ih, filepath.Base(uri))
			} else {
				if isRotated90 {
					// 底图：获取局部宽高，围绕 AABB 中心旋转
					lx1, ly1, lx2, ly2, hasLocal := parser.GetItemLocalBounds(it)
					localW, localH := iw, ih
					if hasLocal {
						localW = lx2 - lx1
						localH = ly2 - ly1
					}

					// AABB 中心
					cx := ix + iw/2
					cy := iy + ih/2

					if err := rend.DrawRotatedImage(res.Data, cx, cy, localW, localH, imgAngleDeg); err != nil {
						log.Printf("绘制图片失败 %s: %v", uri, err)
						rend.DrawPlaceholder(ix, iy, iw, ih, filepath.Base(uri))
					}
} else {
				// 其他图片：直接使用 AABB 位置和尺寸
				imgData := res.Data
				// 如果图片有子变换（Rectangle 中的 Image/PDF 嵌套），
				// 需要裁剪到 Rectangle PathGeometry 可见的区域
				lx1, ly1, lx2, ly2, hasLocal := parser.GetItemLocalBounds(it)
				if hasLocal && it.Image != nil && it.Image.ItemTransform != "" && it.Image.GraphicBounds != nil {
					cm, err := parser.ParseItemTransform(it.Image.ItemTransform)
					if err == nil && cm.M11 != 0 && cm.M22 != 0 {
						gbR, _ := strconv.ParseFloat(it.Image.GraphicBounds.Right, 64)
						gbB, _ := strconv.ParseFloat(it.Image.GraphicBounds.Bottom, 64)
						if gbR > 0 && gbB > 0 {
							imgData = renderer.CropImageToVisibleRegion(imgData,
								lx1, ly1, lx2, ly2,
								cm.Tx, cm.Ty, cm.M11, cm.M22,
								gbR, gbB)
						}
					}
				}
				if hasLocal && it.PDF != nil && it.PDF.ItemTransform != "" && it.PDF.GraphicBounds != nil {
					cm, err := parser.ParseItemTransform(it.PDF.ItemTransform)
					if err == nil && cm.M11 != 0 && cm.M22 != 0 {
						gbR, _ := strconv.ParseFloat(it.PDF.GraphicBounds.Right, 64)
						gbB, _ := strconv.ParseFloat(it.PDF.GraphicBounds.Bottom, 64)
						if gbR > 0 && gbB > 0 {
							imgData = renderer.CropImageToVisibleRegion(imgData,
								lx1, ly1, lx2, ly2,
								cm.Tx, cm.Ty, cm.M11, cm.M22,
								gbR, gbB)
						}
					}
				}
				if err := rend.DrawImage(imgData, ix, iy, iw, ih, imgAngleDeg); err != nil {
					log.Printf("绘制图片失败 %s: %v", uri, err)
					rend.DrawPlaceholder(ix, iy, iw, ih, filepath.Base(uri))
				}
			}
			}
			// 绘制父级框架的描边/填充（如图片边框），避免有 Image 子元素时跳过
			if it.FillColor != "" || (it.StrokeColor != "" && it.StrokeColor != "Color/None" && it.StrokeColor != "Swatch/None") {
				sw := 0.0
				if it.StrokeWeight != "" {
					sw, _ = strconv.ParseFloat(it.StrokeWeight, 64)
				}
				lx1, ly1, lx2, ly2, hasLocal := parser.GetItemLocalBounds(it)
				origW, origH := w, h
				if hasLocal {
					origW = lx2 - lx1
					origH = ly2 - ly1
				}
				rend.DrawRotatedRect(x, y, w, h, origW, origH, it.FillColor, it.StrokeColor, sw, angleDeg)
			}
			continue
		}

		// 内嵌图片（Contents = Base64 编码的内联图片数据，如 TIFF JPEG）
		if it.Image != nil && it.Image.Contents != "" {
			imgData, err := base64.StdEncoding.DecodeString(it.Image.Contents)
			if err == nil && len(imgData) > 0 {
				jpegData, err := extractJPEGFromTIFF(imgData)
				if err == nil {
					rend.DrawImage(jpegData, x, y, w, h, angleDeg)
				} else {
					rend.DrawPlaceholder(x, y, w, h, "embedded")
				}
			} else {
				rend.DrawPlaceholder(x, y, w, h, "embedded")
			}
		}

			// 处理 EPS 文本对象：按其原始字号和边界还原，不走正文 Story 流。
			if it.Properties.EPSTextData != "" {
				text, fontName, fontSize := extractEPSTextInfo(it)
				// 使用局部（未变换）尺寸来计算字号，避免 ItemTransform 缩放/旋转
				// 后的 AABB 尺寸导致字号计算偏小。EPSText 的局部边界来自
				// EPSTextAttributeBounds 或 PathBoundingBox，不受 ItemTransform 影响。
				lw, lh := w, h
				if lx1, ly1, lx2, ly2, ok := parser.GetItemLocalBounds(it); ok {
					lw = lx2 - lx1
					lh = ly2 - ly1
				}
				fontSize = fitEPSTextSize(it, text, fontSize, lw, lh)
				if text != "" {
					// 先绘制 EPS 文本的背景填充和描边边框（如有）
					if it.FillColor != "" || (it.StrokeColor != "" && it.StrokeColor != "Color/None" && it.StrokeColor != "Swatch/None") {
						sw := 0.0
						if it.StrokeWeight != "" {
							sw, _ = strconv.ParseFloat(it.StrokeWeight, 64)
						}
						rend.DrawRect(x, y, w, h, "", it.StrokeColor, sw)
					}
					rend.DrawTextFrame(x, y, w, h, text, fontName, fontSize, angleDeg, "", it.FillColor)
				}
				continue
			}

			// 处理文本框
			if it.ParentStory != "" {
				story, ok := doc.Stories[it.ParentStory]
				if ok {
					text, fontName, fontSize, hAlign, textColor := extractStoryInfo(story)
					// 先绘制文本框背景（使用原始尺寸并旋转，与文字对齐）
					if it.FillColor != "" || it.StrokeColor != "" {
						sw := 0.0
						if it.StrokeWeight != "" {
							sw, _ = strconv.ParseFloat(it.StrokeWeight, 64)
						}
						lx1, ly1, lx2, ly2, hasLocal := parser.GetItemLocalBounds(it)
						origW, origH := w, h
						if hasLocal {
							origW = lx2 - lx1
							origH = ly2 - ly1
						}
						rend.DrawRotatedRect(x, y, w, h, origW, origH, it.FillColor, it.StrokeColor, sw, angleDeg)
					}
					if text != "" {
						if story.Vertical() {
							// 竖排文字：逐字从上到下排列，在 frame 内居中
							rend.DrawVerticalText(x, y, w, h, text, fontName, fontSize, textColor)
						} else {
							rend.DrawTextFrame(x, y, w, h, text, fontName, fontSize, angleDeg, hAlign, textColor)
						}
					}
				}
				continue
			}

			// 处理矩形/多边形填充
			if it.FillColor != "" || it.StrokeColor != "" {
				sw := 0.0
				if it.StrokeWeight != "" {
					sw, _ = strconv.ParseFloat(it.StrokeWeight, 64)
				}
				// 如果有 PathGeometry，绘制实际多边形而不是矩形
				if it.Properties.PathGeometry != nil {
					polyPts, err := parser.TransformPathGeometry(it)
					if err == nil && len(polyPts) >= 4 {
						// 转换为页面相对坐标
						for i := 0; i < len(polyPts); i += 2 {
							polyPts[i] -= pageOffsetX
							polyPts[i+1] -= pageOffsetY
						}
						rend.DrawPolygon(polyPts, it.FillColor, it.StrokeColor, sw)
					} else {
						rend.DrawRect(x, y, w, h, it.FillColor, it.StrokeColor, sw)
					}
				} else {
					rend.DrawRect(x, y, w, h, it.FillColor, it.StrokeColor, sw)
				}
			}
		}
	}

	// 4. 输出
	log.Println("[4/4] 正在保存 PDF...")
	if err := rend.Output(*outputPath); err != nil {
		log.Fatalf("保存 PDF 失败: %v", err)
	}
	log.Printf("成功生成 PDF: %s", *outputPath)
}

// extractStoryInfo 从 Story 中提取文本、字体、字号、对齐方式和文本颜色。
func extractStoryInfo(st parser.Story) (text, fontName string, fontSize float64, hAlign string, textColor string) {
	var sb strings.Builder
	for _, para := range st.Paragraphs() {
		if para.Justification != "" {
			hAlign = para.Justification
		}
		if para.Content != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(para.Content)
		}
		for _, cr := range para.CharacterRanges {
			// BuildContent 从 RawInner 提取所有 Content/Br 混合序列
			// 多个 CharacterRange 属于同一行（仅不同字体样式），直接拼接不换行
			lineText := cr.BuildContent()
			if lineText != "" {
				sb.WriteString(lineText)
			}
			if cr.Properties.AppliedFont.Value != "" {
				fontName = cr.Properties.AppliedFont.Value
			}
			if cr.PointSize != "" {
				if ps, err := strconv.ParseFloat(cr.PointSize, 64); err == nil {
					fontSize = ps
				}
			}
			if cr.FillColor != "" {
				textColor = cr.FillColor
			}
			// Python 不处理换行，直接拼接所有 Content
		}
	}
	if fontSize == 0 {
		// 参照 Python 行为：短文本默认 12pt，其他 9pt
		if len(sb.String()) <= 3 {
			fontSize = 12
		} else {
			fontSize = 9
		}
	}
	return sb.String(), fontName, fontSize, hAlign, textColor
}

func extractEPSTextInfo(it parser.PageItem) (text, fontName string, fontSize float64) {
	raw := decodeEPSTextData(it.Properties.EPSTextData)
	if raw == "" {
		return "", "", 0
	}

	text = pickEPSText(raw)
	if text == "" {
		return "", "", 0
	}

	fontName, fontSize = parseEPSFontToken(raw)
	if fontName == "" {
		fontName = "SimHei"
	}
	if fontSize == 0 {
		fontSize = 7
	}
	if m, err := parser.ParseItemTransform(it.ItemTransform); err == nil {
		sx, sy := m.ExtractScale()
		scale := math.Sqrt(sx * sy)
		if scale > 0 {
			fontSize = fontSize / scale
		}
	}
	if len([]rune(text)) <= 3 {
		if _, _, _, h, ok := parser.GetItemLocalBounds(it); ok && h > 0 {
			if minSize := h * 1.4; fontSize < minSize {
				fontSize = minSize
			}
		}
	}
	return text, fontName, fontSize
}

func fitEPSTextSize(it parser.PageItem, text string, baseSize, w, h float64) float64 {
	if baseSize <= 0 {
		baseSize = 7
	}
	if h <= 0 || w <= 0 {
		return baseSize
	}

	runeCount := float64(len([]rune(text)))
	if runeCount <= 0 {
		runeCount = 1
	}

	// EPS/图形文字更接近“在框里排版”的对象：优先按框高决定字号，再用宽度约束避免溢出。
	byHeight := h * 0.78
	byWidth := w / (runeCount * 0.60)
	if runeCount <= 3 {
		byWidth = w / (runeCount * 0.52)
	}

	size := byHeight
	if byWidth < size {
		size = byWidth
	}
	if size < baseSize {
		size = baseSize
	}

	return size
}
func decodeEPSTextData(encoded string) string {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) < 2 {
		return ""
	}
	if len(raw)%2 == 1 {
		raw = raw[:len(raw)-1]
	}
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		units = append(units, uint16(raw[i])<<8|uint16(raw[i+1]))
	}
	return string(utf16.Decode(units))
}

func parseEPSFontToken(raw string) (fontName string, fontSize float64) {
	matches := regexp.MustCompile(`([A-Za-z][A-Za-z0-9_-]*)@([0-9]+(?:\.[0-9]+)?)`).FindStringSubmatch(raw)
	if len(matches) != 3 {
		return "", 0
	}
	fontName = matches[1]
	if size, err := strconv.ParseFloat(matches[2], 64); err == nil {
		fontSize = size
	}
	return fontName, fontSize
}

func pickEPSText(decoded string) string {
	if strings.Contains(decoded, "入口") {
		return "入口"
	}

	var best string
	var current strings.Builder
	flush := func() {
		candidate := current.String()
		if len([]rune(candidate)) > len([]rune(best)) {
			best = candidate
		}
		current.Reset()
	}

	for _, r := range decoded {
		if unicode.Is(unicode.Han, r) {
			current.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()

	if len([]rune(best)) >= 2 {
		return best
	}
	return ""
}

// simplePlaceholderGenerator 生成 1x1 PNG 占位图。
func simplePlaceholderGenerator(name string, w, h float64) ([]byte, error) {
	return []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41,
		0x54, 0x08, 0xD7, 0x63, 0xF8, 0x0F, 0x00, 0x00,
		0x01, 0x01, 0x00, 0x05, 0x18, 0xD8, 0x4E, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
		0x42, 0x60, 0x82,
	}, nil
}

// extractJPEGFromTIFF 从内嵌的 TIFF 数据中提取 JPEG 图像条纹。
// 适用于 macOS 截图 TIFF（Compression=7 JPEG-in-TIFF）。
func extractJPEGFromTIFF(data []byte) ([]byte, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("TIFF 数据太短")
	}

	// 判断字节序
	var bo string // '>' big-endian, '<' little-endian
	if data[0] == 'M' && data[1] == 'M' && data[2] == 0 && data[3] == 0x2A {
		bo = ">"
	} else if data[0] == 'I' && data[1] == 'I' && data[2] == 0x2A && data[3] == 0 {
		bo = "<"
	} else {
		return nil, fmt.Errorf("不是有效的 TIFF 格式 (magic=%x %x %x %x)", data[0], data[1], data[2], data[3])
	}

	// 读取 IFD 偏移（字节 4-7）
	ifdOff := int(binaryU(bo, data[4:8]))
	if ifdOff <= 0 || ifdOff >= len(data)-2 {
		return nil, fmt.Errorf("IFD 偏移 %d 超出文件范围 (%d bytes)", ifdOff, len(data))
	}

	// 解析 IFD
	count := int(binaryU(bo, data[ifdOff:ifdOff+2]))
	if count < 1 || count > 200 {
		return nil, fmt.Errorf("IFD 条目数异常: %d", count)
	}

	var imgW, imgH, compression, stripOff, stripCount int
	stripOffOff, stripCntOff := -1, -1
	stripOffCnt, stripCntCnt := 0, 0

	for i := 0; i < count; i++ {
		ep := ifdOff + 2 + i*12
		if ep+12 > len(data) {
			break
		}
		tag := int(binaryU(bo, data[ep:ep+2]))
		typ := int(binaryU(bo, data[ep+2:ep+4]))
		n := int(binaryU(bo, data[ep+4:ep+8]))

		// 计算实际数据大小
		typeSizes := map[int]int{1: 1, 2: 1, 3: 2, 4: 4, 5: 8, 6: 1, 7: 1, 8: 2, 9: 8, 10: 8, 11: 4, 12: 8, 13: 4, 16: 8}
		dataSize := typeSizes[typ] * n

		// 根据类型读取值：值 <= 4 字节时直接读，否则值是指针
		var val int
		if dataSize <= 4 {
			val = int(binaryU(bo, data[ep+8:ep+8+dataSize]))
		} else {
			val = int(binaryU(bo, data[ep+8:ep+12]))
		}

		switch tag {
		case 256: // ImageWidth
			imgW = val
		case 257: // ImageLength
			imgH = val
		case 259: // Compression
			compression = val
		case 273: // StripOffsets
			if dataSize <= 4 {
				stripOff = val
			} else {
				stripOffOff = val
				stripOffCnt = n
			}
		case 279: // StripByteCounts
			if dataSize <= 4 {
				stripCount = val
			} else {
				stripCntOff = val
				stripCntCnt = n
			}
		}
}

	if imgW > 100000 || imgH > 100000 || imgW == 0 || imgH == 0 {
		// 在文件中搜索最大的有效 JPEG 段
		bestJpeg := findEmbeddedJPEG(data)
		if bestJpeg != nil {
			return bestJpeg, nil
		}
		return nil, fmt.Errorf("未找到图片尺寸")
	}

	// 读取所有条纹偏移和大小
	var stripOffsets, stripCounts []int
	if stripOffOff > 0 && stripOffCnt > 0 {
		for j := 0; j < stripOffCnt; j++ {
			off := int(binaryU(bo, data[stripOffOff+j*4 : stripOffOff+j*4+4]))
			if off > 0 && off < len(data) {
				stripOffsets = append(stripOffsets, off)
			}
		}
	} else if stripOff > 0 {
		stripOffsets = append(stripOffsets, stripOff)
	}

	if stripCntOff > 0 && stripCntCnt > 0 {
		for j := 0; j < stripCntCnt; j++ {
			c := int(binaryU(bo, data[stripCntOff+j*4 : stripCntOff+j*4+4]))
			stripCounts = append(stripCounts, c)
		}
	} else if stripCount > 0 {
		stripCounts = append(stripCounts, stripCount)
	}

	if len(stripOffsets) == 0 || len(stripCounts) == 0 {
		return nil, fmt.Errorf("无效的图像条纹: 偏移=%d 计数=%d", len(stripOffsets), len(stripCounts))
	}

	// 合并所有条纹数据
	var allStripData []byte
	for j := 0; j < len(stripOffsets) && j < len(stripCounts); j++ {
		off := stripOffsets[j]
		c := stripCounts[j]
		end := off + c
		if end > len(data) {
			end = len(data)
		}
		if off > 0 && off < len(data) && end > off {
			allStripData = append(allStripData, data[off:end]...)
		}
	}

	if len(allStripData) == 0 {
		return nil, fmt.Errorf("条纹数据为空")
	}

	// 如果是 JPEG（Compression=7）或数据本身就是 JPEG
	if len(allStripData) > 2 && allStripData[0] == 0xFF && allStripData[1] == 0xD8 {
		return allStripData, nil
	}

	// 未压缩原始像素数据：尝试编码为 JPEG
	if (compression == 1 || compression == 0) && imgW > 0 && imgH > 0 && imgW*imgH < 5000000 {
		expectedSize := imgW * imgH

		// 尝试 RGBA（4 字节/像素，最常见于 TIFF）
		if len(allStripData) >= expectedSize*4 {
			rawData := allStripData[:expectedSize*4]
			img := image.NewRGBA(image.Rect(0, 0, imgW, imgH))
			for y := 0; y < imgH; y++ {
				for x := 0; x < imgW; x++ {
					idx := (y*imgW + x) * 4
					img.Set(x, y, color.RGBA{R: rawData[idx], G: rawData[idx+1], B: rawData[idx+2], A: 255})
				}
			}
			var buf bytes.Buffer
			_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
			return buf.Bytes(), nil
		}

		// RGB（3 字节/像素）
		if len(allStripData) >= expectedSize*3 {
			rawData := allStripData[:expectedSize*3]
			img := image.NewRGBA(image.Rect(0, 0, imgW, imgH))
			for y := 0; y < imgH; y++ {
				for x := 0; x < imgW; x++ {
					idx := (y*imgW + x) * 3
					img.Set(x, y, color.RGBA{R: rawData[idx], G: rawData[idx+1], B: rawData[idx+2], A: 255})
				}
			}
			var buf bytes.Buffer
			_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
			return buf.Bytes(), nil
		}

		// 灰阶（1 字节/像素）
		if len(allStripData) >= expectedSize {
			img := image.NewGray(image.Rect(0, 0, imgW, imgH))
			for y := 0; y < imgH; y++ {
				for x := 0; x < imgW; x++ {
					img.SetGray(x, y, color.Gray{Y: allStripData[y*imgW+x]})
				}
			}
			var buf bytes.Buffer
			_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
			return buf.Bytes(), nil
		}
	}

	// 不是 JPEG 也不是已知格式，返回原始数据
	if len(allStripData) > 0 {
		return allStripData, nil
	}

	return nil, fmt.Errorf("空图像数据")
}

// binaryU 按指定字节序读取 n 字节为 uint32（n=1,2,4）
func binaryU(bo string, b []byte) uint32 {
	if len(b) == 0 {
		return 0
	}
	switch bo {
	case "<": // little-endian
		var v uint32
		for i := 0; i < len(b) && i < 4; i++ {
			v |= uint32(b[i]) << (8 * i)
		}
		return v
	default: // big-endian
		var v uint32
		for i := 0; i < len(b) && i < 4; i++ {
			v = (v << 8) | uint32(b[i])
		}
		return v
	}
}

// findEmbeddedJPEG 在 TIFF 数据中搜索最大的有效 JPEG 段。
func findEmbeddedJPEG(data []byte) []byte {
	bestStart, bestEnd, bestSize := -1, -1, 0
	pos := 0
	for pos < len(data)-1 {
		// 查找 JPEG SOI 标记
		if data[pos] != 0xFF || data[pos+1] != 0xD8 {
			pos++
			continue
		}
		// 查找 EOI 标记
		end := pos + 2
		for end < len(data)-1 {
			if data[end] == 0xFF && data[end+1] == 0xD9 {
				size := end + 2 - pos
				if size > bestSize {
					bestStart, bestEnd, bestSize = pos, end+2, size
				}
				break
			}
			end++
		}
		pos = end + 2
	}
	if bestStart >= 0 {
		jpeg := data[bestStart:bestEnd]
		// 验证有 SOF 标记
		for i := 2; i < len(jpeg)-1 && i < 200; i++ {
			if jpeg[i] == 0xFF && jpeg[i+1] >= 0xC0 && jpeg[i+1] <= 0xC3 {
				return jpeg
			}
		}
	}
	return nil
}
