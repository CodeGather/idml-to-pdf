package main

import (
	"encoding/base64"
	"flag"
	"fmt"
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
		var masterYOffset float64
		if len(sp.Pages()) > 0 {
			px, py, _, _, err := parser.GetPageBounds(sp.Pages()[0])
			if err == nil {
				pageOffsetX, pageOffsetY = px, py
			}
			masterYOffset = parser.GetMasterPageYOffset(sp.Pages()[0])
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
			igx1, igy1, igx2, _, imgAngle, err := parser.ComputeImageGlobalBounds(it)
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
			ih := gy2 - igy1
			imgAngleDeg := imgAngle * 180 / math.Pi

				res := loader.Resolve(uri, doc.EmbeddedGraphics)
				if res.Err != nil {
					log.Printf("素材缺失: %s", filepath.Base(uri))
					rend.DrawPlaceholder(ix, iy, iw, ih, filepath.Base(uri))
				} else {
					if err := rend.DrawImage(res.Data, ix, iy, iw, ih, imgAngleDeg); err != nil {
						log.Printf("绘制图片失败 %s: %v", uri, err)
						rend.DrawPlaceholder(ix, iy, iw, ih, filepath.Base(uri))
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
							// MasterPageTransform 的 Y 偏移需要叠加到竖排文字上
							vertY := y - masterYOffset
							if vertY < 0 {
								vertY = y
							}
							rend.DrawVerticalText(x, vertY, w, h, text, fontName, fontSize, textColor)
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
