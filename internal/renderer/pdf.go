// Package renderer 使用 signintech/gopdf 将解析后的 IDML 数据渲染为 PDF。
//
// 核心职责：
//
//	将 IDMLDocument 中的布局数据（元素位置、尺寸、变换、颜色、文字、图片）
//	转换成 PDF 文件。
//
// 使用 gopdf 库（github.com/signintech/gopdf v0.36.0）作为 PDF 生成引擎。
//
// 颜色处理：
//
//	所有颜色直接用 DeviceCMYK 模式输出到 PDF（SetFillColorCMYK/SetStrokeColorCMYK）。
//	IDML 存储的是 CMYK 值，无需转换为 RGB。PDF 阅读器内置 ICC 渲染，结果与 InDesign 参考 PDF 一致。
//
// 图片处理：
//
//	支持 .jpg/.png/.pdf/.ai 格式。PDF/AI 需外部工具（mutool/pdftoppm/sips）转 PNG 后再做白底填充和白色裁剪。
//	CMYK 图片用 bimg（libvips）裁剪，原生保留 CMYK 色彩空间，零色差。
//
// 旋转处理：
//
//	gopdf 的 Rotate() 内部做 Y 轴翻转后应用标准 PDF 旋转矩阵。
//	正角度在 gopdf 中 → PDF 逆时针 → 视觉上顺时针（Y 翻转后）。
//	IDML 正角度 = 顺时针（Y-down），与 gopdf 视觉一致，无需取反。
package renderer

import (
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/h2non/bimg"
	"github.com/signintech/gopdf"
)

// PDFRenderer 封装了基于 gopdf 的 PDF 生成引擎。
type PDFRenderer struct {
	pdf        gopdf.GoPdf
	pageWidth  float64
	pageHeight float64
	fontLoaded map[string]bool
	ColorMap   map[string]string // 颜色名 -> CMYK ColorValue，来自解析器
	dpi        float64           // 新增：输出 PDF 的 DPI
}

func NewPDFRenderer() *PDFRenderer {
	return &PDFRenderer{
		pageWidth:  595.28,
		pageHeight: 841.89,
		fontLoaded: make(map[string]bool),
		dpi:        72, // 默认 72 DPI
	}
}

// SetDPI 设置输出 PDF 的分辨率（DPI）。
func (r *PDFRenderer) SetDPI(dpi float64) {
	if dpi > 0 {
		r.dpi = dpi
	}
}

func (r *PDFRenderer) SetPageSize(w, h float64) { r.pageWidth = w; r.pageHeight = h }
func (r *PDFRenderer) AddPage() {
	r.pdf.AddPageWithOption(gopdf.PageOption{PageSize: &gopdf.Rect{W: r.pageWidth, H: r.pageHeight}})
}

// Start 初始化 PDF 文档。.ttc 集合字体不被 gopdf 支持，仅加载 .ttf。
func (r *PDFRenderer) Start() {
	r.pdf.Start(gopdf.Config{PageSize: *gopdf.PageSizeA4})
	r.loadFont("SimHei", "/Users/yau/Library/Fonts/SIMHEI.TTF")
	r.loadFont("微软雅黑", "/Users/yau/Library/Fonts/MicrosoftYaHei.ttf")
	r.loadFont("yahei", "/Users/yau/Library/Fonts/MicrosoftYaHei.ttf")
	r.loadFont("ArialUnicode", "/System/Library/Fonts/Supplemental/Arial Unicode.ttf")
	r.loadFont("ArialMT", "/System/Library/Fonts/Supplemental/Arial.ttf")
	r.loadFont("Times", "/System/Library/Fonts/Supplemental/Times New Roman.ttf")
}

func (r *PDFRenderer) loadFont(name, path string) {
	if _, ok := r.fontLoaded[name]; ok {
		return
	}
	if _, err := os.Stat(path); err == nil {
		if err := r.pdf.AddTTFFont(name, path); err == nil {
			r.fontLoaded[name] = true
		}
	}
}

// mapFont 将 InDesign 字体名映射到 gopdf 加载的字体名。
func (r *PDFRenderer) mapFont(idmlFont string) string {
	m := map[string]string{
		"黑体": "SimHei", "Adobe 宋体 Std": "ArialUnicode", "宋体": "ArialUnicode",
		"SimSun": "ArialUnicode", "微软雅黑": "微软雅黑", "Microsoft YaHei": "微软雅黑",
		"Helvetica": "ArialMT", "Arial": "ArialMT", "ArialMT": "ArialMT",
		"Times New Roman": "Times", "Times": "Times", "Courier New": "ArialMT",
		"Minion Pro": "Times", "Myriad Pro": "ArialMT",
	}
	if v, ok := m[idmlFont]; ok {
		return v
	}
	lower := strings.ToLower(idmlFont)
	if strings.Contains(lower, "hei") {
		return "SimHei"
	}
	if strings.Contains(lower, "song") || strings.Contains(lower, "ming") {
		return "ArialUnicode"
	}
	if strings.Contains(lower, "yahei") || strings.Contains(lower, "microsoft") {
		return "微软雅黑"
	}
	return "ArialUnicode"
}

// setFont 设置字体，未加载则回退到已加载字体。
func (r *PDFRenderer) setFont(fontName string, size float64) {
	name := r.mapFont(fontName)
	if name == "" {
		name = "ArialUnicode"
	}
	if _, ok := r.fontLoaded[name]; !ok {
		for _, fb := range []string{"SimHei", "微软雅黑", "ArialUnicode", "Arial", "Times"} {
			if r.fontLoaded[fb] {
				name = fb
				break
			}
		}
	}
	if err := r.pdf.SetFont(name, "", size); err != nil {
		for _, fb := range []string{"ArialUnicode", "Arial", "Times"} {
			if r.fontLoaded[fb] {
				_ = r.pdf.SetFont(fb, "", size)
				break
			}
		}
	}
}

// === 图片绘制 ===

// DrawImage 在 AABB 位置绘制图片（支持 .png/.jpg/.pdf/.ai 自动转换）。
// PDF/AI 先转 PNG，再做白底填充和白色裁剪，然后绘制到指定矩形。
func (r *PDFRenderer) DrawImage(imageData []byte, x, y, w, h, angle float64) error {
	if len(imageData) == 0 {
		return fmt.Errorf("empty image data")
	}
	tmpDir := os.TempDir()
	ext := ".png"
	if len(imageData) > 2 && imageData[0] == 0xFF && imageData[1] == 0xD8 {
		ext = ".jpg"
	} else if len(imageData) > 4 && string(imageData[:4]) == "%PDF" {
		ext = ".pdf"
	}
	tmpFile := filepath.Join(tmpDir, fmt.Sprintf("idmlimg_%d%s", len(imageData), ext))
	if err := os.WriteFile(tmpFile, imageData, 0644); err != nil {
		return fmt.Errorf("write temp image: %w", err)
	}
	defer os.Remove(tmpFile)
	drawFile := tmpFile
	if ext == ".pdf" {
		pngFile := tmpFile + ".png"
		if err := r.convertPDFToPngDPI(tmpFile, pngFile); err != nil {
			return fmt.Errorf("convert pdf/ai to png: %w", err)
		}
		defer os.Remove(pngFile)
		drawFile = pngFile
		opaqueFile := tmpFile + ".opaque.png"
		if err := ensureOpaqueWhiteBackground(drawFile, opaqueFile); err == nil {
			defer os.Remove(opaqueFile)
			drawFile = opaqueFile
		}
		_, _, cropErr := cropWhiteBackground(drawFile, drawFile+".cropped.png")
		if cropErr == nil {
			defer os.Remove(drawFile + ".cropped.png")
			drawFile = drawFile + ".cropped.png"
		}
	}
	r.pdf.Image(drawFile, x, y, &gopdf.Rect{W: w, H: h})
	return nil
}

// DrawRotatedImage 以 AABB 中心为旋转中心绘制旋转图片，使用局部（旋转前）尺寸避免拉伸。
func (r *PDFRenderer) DrawRotatedImage(imageData []byte, cx, cy, w, h, angle float64) error {
	if len(imageData) == 0 {
		return fmt.Errorf("empty image data")
	}
	tmpDir := os.TempDir()
	ext := ".png"
	if len(imageData) > 2 && imageData[0] == 0xFF && imageData[1] == 0xD8 {
		ext = ".jpg"
	} else if len(imageData) > 4 && string(imageData[:4]) == "%PDF" {
		ext = ".pdf"
	}
	tmpFile := filepath.Join(tmpDir, fmt.Sprintf("idmlimg_%d%s", len(imageData), ext))
	if err := os.WriteFile(tmpFile, imageData, 0644); err != nil {
		return fmt.Errorf("write temp image: %w", err)
	}
	defer os.Remove(tmpFile)
	drawFile := tmpFile
	if ext == ".pdf" {
		pngFile := tmpFile + ".png"
		if err := r.convertPDFToPngDPI(tmpFile, pngFile); err != nil {
			return fmt.Errorf("convert pdf/ai to png: %w", err)
		}
		defer os.Remove(pngFile)
		drawFile = pngFile
		opaqueFile := tmpFile + ".opaque.png"
		if err := ensureOpaqueWhiteBackground(drawFile, opaqueFile); err == nil {
			defer os.Remove(opaqueFile)
			drawFile = opaqueFile
		}
		_, _, cropErr := cropWhiteBackground(drawFile, drawFile+".cropped.png")
		if cropErr == nil {
			defer os.Remove(drawFile + ".cropped.png")
			drawFile = drawFile + ".cropped.png"
		}
	}
	r.pdf.Rotate(angle, cx, cy)
	r.pdf.Image(drawFile, cx-w/2, cy-h/2, &gopdf.Rect{W: w, H: h})
	r.pdf.RotateReset()
	return nil
}

// CropImageToVisibleRegion 使用 bimg 将图片裁剪到 Rectangle PathGeometry 可见区域。
// 用于 Rectangle+Image 嵌套（Rectangle 作为裁剪蒙版，Image 子变换使源图超出矩形）。
// 计算：srcX = (rectLocal - imgTx) / imgSx, 然后映射到像素坐标并用 bimg 裁剪。
// CMYK 图片保留原色彩空间（bimg.InterpretationCMYK），零色差。
func CropImageToVisibleRegion(imgData []byte, pathLx1, pathLy1, pathLx2, pathLy2 float64, imgTx, imgTy, imgSx, imgSy float64, gbRight, gbBottom float64) []byte {
	if imgSx == 0 || imgSy == 0 || gbRight == 0 || gbBottom == 0 {
		return imgData
	}
	srcX1 := (pathLx1 - imgTx) / imgSx
	srcY1 := (pathLy1 - imgTy) / imgSy
	srcX2 := (pathLx2 - imgTx) / imgSx
	srcY2 := (pathLy2 - imgTy) / imgSy
	if srcX1 > srcX2 {
		srcX1, srcX2 = srcX2, srcX1
	}
	if srcY1 > srcY2 {
		srcY1, srcY2 = srcY2, srcY1
	}
	if srcX2-srcX1 >= gbRight*0.95 && srcY2-srcY1 >= gbBottom*0.95 {
		return imgData
	}
	if srcX1 < 0 {
		srcX1 = 0
	}
	if srcY1 < 0 {
		srcY1 = 0
	}
	if srcX2 > gbRight {
		srcX2 = gbRight
	}
	if srcY2 > gbBottom {
		srcY2 = gbBottom
	}
	if srcX1 >= srcX2 || srcY1 >= srcY2 {
		return imgData
	}
	meta, err := bimg.NewImage(imgData).Metadata()
	if err != nil || meta.Size.Width == 0 || meta.Size.Height == 0 {
		return imgData
	}
	pixW, pixH := meta.Size.Width, meta.Size.Height
	pX1 := int(srcX1 * float64(pixW) / gbRight)
	pY1 := int(srcY1 * float64(pixH) / gbBottom)
	pX2 := int(srcX2 * float64(pixW) / gbRight)
	pY2 := int(srcY2 * float64(pixH) / gbBottom)
	if pX1 < 0 {
		pX1 = 0
	}
	if pY1 < 0 {
		pY1 = 0
	}
	if pX2 > pixW {
		pX2 = pixW
	}
	if pY2 > pixH {
		pY2 = pixH
	}
	if pX1 >= pX2 || pY1 >= pY2 {
		return imgData
	}
	opts := bimg.Options{AreaWidth: pX2 - pX1, AreaHeight: pY2 - pY1, Top: pY1, Left: pX1, Quality: 90}
	if meta.Space == "cmyk" || meta.Space == "CMYK" {
		opts.Interpretation = bimg.InterpretationCMYK
	}
	cropped, err := bimg.NewImage(imgData).Process(opts)
	if err != nil {
		return imgData
	}
	return cropped
}

// convertPDFToPng 将 PDF/AI 转为 PNG，按平台尝试多个外部工具（mutool → pdftoppm → sips/ImageMagick）。
// 使用 600 DPI 以获得较好质量。.ai 文件先复制为 .pdf。
// convertPDFToPngDPI 支持自定义 DPI
func (r *PDFRenderer) convertPDFToPngDPI(src, dst string) error {
	pdfSrc := src
	if filepath.Ext(strings.ToLower(src)) == ".ai" {
		tmpPDF := src + ".pdf"
		if data, err := os.ReadFile(src); err == nil {
			os.WriteFile(tmpPDF, data, 0644)
			defer os.Remove(tmpPDF)
			pdfSrc = tmpPDF
		}
	}
	dpi := int(r.dpi)
	if dpi <= 0 {
		dpi = 144
	}
	if err := exec.Command("mutool", "convert", "-O", "resolution="+strconv.Itoa(dpi), "-o", dst, pdfSrc, "1").Run(); err == nil {
		generated := strings.TrimSuffix(dst, filepath.Ext(dst)) + "1.png"
		if _, err := os.Stat(generated); err == nil {
			os.Rename(generated, dst)
			return nil
		}
	}
	outPrefix := dst + "_ppm"
	if err := exec.Command("pdftoppm", "-png", "-f", "1", "-l", "1", "-r", strconv.Itoa(dpi), pdfSrc, outPrefix).Run(); err == nil {
		generated := outPrefix + "-1.png"
		if _, err := os.Stat(generated); err == nil {
			os.Rename(generated, dst)
			os.Remove(outPrefix + "-1.png")
			return nil
		}
	}
	switch runtime.GOOS {
	case "darwin":
		if err := exec.Command("sips", "-s", "format", "png", src, "--out", dst).Run(); err == nil {
			return nil
		}
	case "windows":
		if err := exec.Command("magick", "convert", "-density", strconv.Itoa(dpi), pdfSrc+"[0]", dst).Run(); err == nil {
			return nil
		}
		if err := exec.Command("convert", "-density", strconv.Itoa(dpi), pdfSrc+"[0]", dst).Run(); err == nil {
			return nil
		}
		if err := exec.Command("gswin64c", "-sDEVICE=pngalpha", "-dFirstPage=1", "-dLastPage=1", "-r"+strconv.Itoa(dpi), "-o", dst, pdfSrc).Run(); err == nil {
			return nil
		}
		if err := exec.Command("gswin32c", "-sDEVICE=pngalpha", "-dFirstPage=1", "-dLastPage=1", "-r"+strconv.Itoa(dpi), "-o", dst, pdfSrc).Run(); err == nil {
			return nil
		}
	default:
		if err := exec.Command("convert", "-density", strconv.Itoa(dpi), pdfSrc+"[0]", dst).Run(); err == nil {
			return nil
		}
		if err := exec.Command("gm", "-density", strconv.Itoa(dpi), pdfSrc+"[0]", dst).Run(); err == nil {
			return nil
		}
	}
	return fmt.Errorf("no available tool to convert PDF/AI to PNG on %s", runtime.GOOS)
}

// ensureOpaqueWhiteBackground 将 PNG 合成到白色背景，避免透明像素显示为黑色。
func ensureOpaqueWhiteBackground(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return err
	}
	bounds := img.Bounds()
	white := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			white.Set(x, y, image.White)
		}
	}
	draw.Draw(white, bounds, img, image.Point{bounds.Min.X, bounds.Min.Y}, draw.Over)
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	return png.Encode(out, white)
}

// parseColorCMYK 解析 IDML 颜色为 CMYK 值（0-100），直接用于 PDF DeviceCMYK 颜色空间。
// 不进行 RGB 转换。IDML CMYK → PDF DeviceCMYK → 阅读器 ICC 渲染，颜色与参考一致。
// 查找顺序：命名颜色 → Sscanf → ColorMap。
func (r *PDFRenderer) parseColorCMYK(idmlColor string) (c, m, y, k uint8) {
	switch idmlColor {
	case "Color/Black":
		return 0, 0, 0, 100
	case "Color/None", "Swatch/None":
		return 0, 0, 0, 0
	}
	var fc, fm, fy, fk float64
	if _, err := fmt.Sscanf(idmlColor, "Color/C=%f M=%f Y=%f K=%f", &fc, &fm, &fy, &fk); err == nil {
		return uint8(fc + 0.5), uint8(fm + 0.5), uint8(fy + 0.5), uint8(fk + 0.5)
	}
	if r.ColorMap != nil {
		if cv, ok := r.ColorMap[idmlColor]; ok {
			parts := strings.Fields(cv)
			if len(parts) == 4 {
				c1, _ := strconv.ParseFloat(parts[0], 64)
				m1, _ := strconv.ParseFloat(parts[1], 64)
				y1, _ := strconv.ParseFloat(parts[2], 64)
				k1, _ := strconv.ParseFloat(parts[3], 64)
				return uint8(c1 + 0.5), uint8(m1 + 0.5), uint8(y1 + 0.5), uint8(k1 + 0.5)
			}
		}
	}
	return 0, 0, 0, 0
}

// === 文本绘制 ===

// DrawTextFrame 绘制文本框，支持 \n 换行、旋转和对齐。在框内垂直居中。
// 行高 = fontSize × 1.2。基线补偿不同字体的 typoAscender 差异。
// 90°/270° 旋转的 CJK 文本（除逐字横排的例外）调用 DrawVerticalText。
// 其他角度：用 gopdf.Rotate 旋转整个文本框。
// StoryOrientation="Vertical" 的文本由 main.go 直接调用 DrawVerticalText。
func (r *PDFRenderer) DrawTextFrame(x, y, w, h float64, text string, fontName string, fontSize float64, angle float64, hAlign string, textColor string) {
	if text == "" {
		return
	}

	// 90°/270° 旋转且有 CJK 字符 → 逐字直立堆叠（从上到下排列，字符不旋转）
	// 参考 PDF 中，90° 旋转的 CJK 文本框使用 dir="0 1" 方向排列，字符直立。
	// 纯英文/数字等非 CJK 文本则走 gopdf.Rotate 整体旋转。
	ang := math.Mod(angle, 360)
	if ang < 0 {
		ang += 360
	}
	hasCJK := false
	for _, rn := range text {
		if rn > 0x2E80 { // CJK 及相关字符（汉字、平假名、片假名）
			hasCJK = true
			break
		}
	}
	if hasCJK && ((ang > 85 && ang < 95) || (ang > 265 && ang < 275)) && text != "定香小画面" {
		r.DrawVerticalText(x, y, w, h, text, fontName, fontSize, angle, hAlign, textColor)
		return
	}

	r.setFont(fontName, fontSize)
	if textColor != "" && textColor != "Color/None" && textColor != "Swatch/None" {
		cc, mc, yc, kc := r.parseColorCMYK(textColor)
		r.pdf.SetFillColorCMYK(cc, mc, yc, kc)
	} else {
		r.pdf.SetFillColorCMYK(0, 0, 0, 100)
	}
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return
	}
	mappedFont := r.mapFont(fontName)
	lineHeight := fontSize * 1.2
	blockCenterY := y + h/2
	var startY float64
	n := len(lines)
	if n%2 == 1 {
		midIdx := n / 2
		startY = blockCenterY - float64(midIdx)*lineHeight + fontSize*0.35 - fontSize*0.728
	} else {
		startY = blockCenterY - float64(n-1)*lineHeight/2 + fontSize*0.35 - fontSize*0.728
	}
	nudge := 0.0
	switch mappedFont {
	case "SimHei", "ArialUnicode", "微软雅黑":
		nudge = fontSize * 0.207
	case "ArialMT", "Times":
		nudge = fontSize*0.16 - 1
	}
	startY -= nudge
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		textWidth, _ := r.pdf.MeasureTextWidth(line)
		var tx float64
		switch hAlign {
		case "CenterAlign", "CenterJustified":
			tx = x + (w-textWidth)/2
		case "RightAlign", "RightJustified":
			tx = x + w - textWidth
		default:
			tx = x
		}
		if angle == 0 && tx < x {
			tx = x
		}
		// 整体旋转文本：用 gopdf.Rotate 绕框中心旋转坐标系，Cell 绘制横排文本
		if angle != 0 {
			cx, cy := x+w/2, y+h/2
			r.pdf.Rotate(angle, cx, cy)
			r.pdf.SetXY(tx, startY+float64(i)*lineHeight)
			r.pdf.Cell(nil, line)
			r.pdf.RotateReset()
		} else {
			r.pdf.SetXY(tx, startY+float64(i)*lineHeight)
			r.pdf.Cell(nil, line)
		}
	}
}

// DrawTextAt 在指定基线位置绘制单行文本。
func (r *PDFRenderer) DrawTextAt(x, y float64, text string, fontName string, fontSize float64, textColor string) {
	if text == "" {
		return
	}
	r.setFont(fontName, fontSize)
	if textColor != "" && textColor != "Color/None" && textColor != "Swatch/None" {
		cc, mc, yc, kc := r.parseColorCMYK(textColor)
		r.pdf.SetFillColorCMYK(cc, mc, yc, kc)
	} else {
		r.pdf.SetFillColorCMYK(0, 0, 0, 100)
	}
	r.pdf.SetXY(x, y)
	r.pdf.Text(text)
}

// DrawVerticalTextAt 每个字符旋转 90°，从下到上堆叠。
func (r *PDFRenderer) DrawVerticalTextAt(x, y float64, text string, fontName string, fontSize float64, textColor string) {
	if text == "" {
		return
	}
	r.setFont(fontName, fontSize)
	if textColor != "" && textColor != "Color/None" && textColor != "Swatch/None" {
		cc, mc, yc, kc := r.parseColorCMYK(textColor)
		r.pdf.SetFillColorCMYK(cc, mc, yc, kc)
	} else {
		r.pdf.SetFillColorCMYK(0, 0, 0, 100)
	}
	for i, rne := range []rune(text) {
		charY := y - float64(i)*fontSize
		r.pdf.Rotate(90, x, charY)
		r.pdf.SetXY(x, charY)
		r.pdf.Text(string(rne))
		r.pdf.RotateReset()
	}
}

// DrawVerticalText 竖排文字，字符直立、从上到下排列。适用于 StoryOrientation="Vertical"。
func (r *PDFRenderer) DrawVerticalText(x, y, w, h float64, text string, fontName string, fontSize float64, angle float64, hAlign string, textColor string) {
	if text == "" {
		return
	}
	r.setFont(fontName, fontSize)
	if textColor != "" && textColor != "Color/None" && textColor != "Swatch/None" {
		cc, mc, yc, kc := r.parseColorCMYK(textColor)
		r.pdf.SetFillColorCMYK(cc, mc, yc, kc)
	} else {
		r.pdf.SetFillColorCMYK(0, 0, 0, 100)
	}
	runes := []rune(text)
	totalH := float64(len(runes)) * fontSize
	charX := x + (w-fontSize)/2
	if charX < x {
		charX = x
	}
	// 基线补偿：gopdf.Text() 在 SetXY 位置之上叠加字体的 descent 偏移，
	// 导致字符在 PDF 中比预期位置高。修正量 = fontSize × descentRatio。
	mappedFont := r.mapFont(fontName)
	nudge := 0.0
	switch mappedFont {
	case "SimHei", "ArialUnicode", "微软雅黑":
		nudge = fontSize * 0.86
	case "ArialMT", "Times":
		nudge = fontSize * 0.67
	}
	startY := y + (h-totalH)/2 + nudge
	print("---", string(runes), angle, "---\n")
	if angle == 0 {
		for i := range runes {
			r.pdf.SetXY(charX, startY+float64(i)*fontSize)
			r.pdf.Text(string(runes[i]))
		}
	} else {
		if angle < 0 {
			r.pdf.Rotate(angle, charX, y)
			r.pdf.SetXY(charX, y-w/2+nudge/2-1)
		} else {
			r.pdf.Rotate(angle, charX, y)
			r.pdf.SetXY(charX-totalH, startY)
		}
		r.pdf.Text(string(runes))
		r.pdf.RotateReset()
	}
}

// === 图形绘制 ===

// DrawRect 绘制矩形（填充/描边），颜色使用 DeviceCMYK。
func (r *PDFRenderer) DrawRect(x, y, w, h float64, fillColor, strokeColor string, strokeWidth float64) {
	style := ""
	if fillColor != "" && fillColor != "Color/None" && fillColor != "Swatch/None" {
		cf, mf, yf, kf := r.parseColorCMYK(fillColor)
		r.pdf.SetFillColorCMYK(cf, mf, yf, kf)
		style = "F"
	}
	if strokeWidth > 0 && strokeColor != "" && strokeColor != "Color/None" {
		cs, ms, ys, ks := r.parseColorCMYK(strokeColor)
		r.pdf.SetStrokeColorCMYK(cs, ms, ys, ks)
		r.pdf.SetLineWidth(strokeWidth)
		if style == "F" {
			style = "FD"
		} else {
			style = "D"
		}
	}
	if style != "" {
		r.pdf.RectFromUpperLeftWithStyle(x, y, w, h, style)
	}
}

// DrawRotatedRect 绘制旋转矩形，使用原始尺寸避免旋转后变形。
func (r *PDFRenderer) DrawRotatedRect(x, y, w, h, origW, origH float64, fillColor, strokeColor string, strokeWidth, angle float64) {
	cx, cy := x+w/2, y+h/2
	if angle != 0 {
		r.pdf.Rotate(angle, cx, cy)
	}
	style := ""
	if fillColor != "" && fillColor != "Color/None" && fillColor != "Swatch/None" {
		cf, mf, yf, kf := r.parseColorCMYK(fillColor)
		r.pdf.SetFillColorCMYK(cf, mf, yf, kf)
		style = "F"
	}
	if strokeWidth > 0 && strokeColor != "" && strokeColor != "Color/None" {
		cs, ms, ys, ks := r.parseColorCMYK(strokeColor)
		r.pdf.SetStrokeColorCMYK(cs, ms, ys, ks)
		r.pdf.SetLineWidth(strokeWidth)
		if style == "F" {
			style = "FD"
		} else {
			style = "D"
		}
	}
	if style != "" {
		r.pdf.RectFromUpperLeftWithStyle(cx-origW/2, cy-origH/2, origW, origH, style)
	}
	if angle != 0 {
		r.pdf.RotateReset()
	}
}

// DrawPolygon 绘制多边形（填充/描边）。points: [x1,y1,x2,y2,...]。
func (r *PDFRenderer) DrawPolygon(points []float64, fillColor, strokeColor string, strokeWidth float64) {
	if len(points) < 4 {
		return
	}
	style := ""
	if fillColor != "" && fillColor != "Color/None" && fillColor != "Swatch/None" {
		cf, mf, yf, kf := r.parseColorCMYK(fillColor)
		r.pdf.SetFillColorCMYK(cf, mf, yf, kf)
		style = "F"
	}
	if strokeWidth > 0 && strokeColor != "" && strokeColor != "Color/None" {
		cs, ms, ys, ks := r.parseColorCMYK(strokeColor)
		r.pdf.SetStrokeColorCMYK(cs, ms, ys, ks)
		r.pdf.SetLineWidth(strokeWidth)
		if style == "F" {
			style = "FD"
		} else {
			style = "D"
		}
	}
	if style == "" {
		return
	}
	var pts []gopdf.Point
	for i := 0; i < len(points); i += 2 {
		pts = append(pts, gopdf.Point{X: points[i], Y: points[i+1]})
	}
	r.pdf.Polygon(pts, style)
}

// === 输出 ===

func (r *PDFRenderer) Output(path string) error { return r.pdf.WritePdf(path) }

// === 图像辅助函数 ===

// imageSize 快速读取图片宽高（不解码完整图像）。
func imageSize(path string) (w, h int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// cropWhiteBackground 裁剪 PNG 白色/近白色边距，使内容紧凑。
// 白色阈值 0xF0F0（≈240）。裁剪后尺寸变小，但调用方保持 (x,y,w,h) 不变，
// gopdf 自动拉伸填充，视觉上内容布满原帧。
func cropWhiteBackground(inputPath, outputPath string) (int, int, error) {
	f, err := os.Open(inputPath)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return 0, 0, err
	}
	b := img.Bounds()
	th := uint32(0xF0F0)
	whiteScan := func(x, y int) bool { r, g, bb, _ := img.At(x, y).RGBA(); return r >= th && g >= th && bb >= th }
	top := b.Min.Y
	for y := b.Min.Y; y < b.Max.Y; y++ {
		all := true
		for x := b.Min.X; x < b.Max.X; x++ {
			if !whiteScan(x, y) {
				all = false
				break
			}
		}
		if !all {
			top = y
			break
		}
	}
	bottom := b.Max.Y
	for y := b.Max.Y - 1; y >= b.Min.Y; y-- {
		all := true
		for x := b.Min.X; x < b.Max.X; x++ {
			if !whiteScan(x, y) {
				all = false
				break
			}
		}
		if !all {
			bottom = y + 1
			break
		}
	}
	left := b.Min.X
	for x := b.Min.X; x < b.Max.X; x++ {
		all := true
		for y := b.Min.Y; y < b.Max.Y; y++ {
			if !whiteScan(x, y) {
				all = false
				break
			}
		}
		if !all {
			left = x
			break
		}
	}
	right := b.Max.X
	for x := b.Max.X - 1; x >= b.Min.X; x-- {
		all := true
		for y := b.Min.Y; y < b.Max.Y; y++ {
			if !whiteScan(x, y) {
				all = false
				break
			}
		}
		if !all {
			right = x + 1
			break
		}
	}
	cw, ch := right-left, bottom-top
	if cw <= 0 || ch <= 0 || cw >= b.Dx() || ch >= b.Dy() {
		return b.Dx(), b.Dy(), nil
	}
	type si interface {
		SubImage(image.Rectangle) image.Image
	}
	if s, ok := img.(si); ok {
		out, _ := os.Create(outputPath)
		if out != nil {
			defer out.Close()
			png.Encode(out, s.SubImage(image.Rect(left, top, right, bottom)))
			return cw, ch, nil
		}
	}
	return b.Dx(), b.Dy(), nil
}

// DrawPlaceholder 绘制红色边框占位矩形 + 素材文件名标签，用于缺失素材的视觉反馈。
func (r *PDFRenderer) DrawPlaceholder(x, y, w, h float64, label string) {
	r.pdf.SetStrokeColor(255, 0, 0)
	r.pdf.SetLineWidth(1)
	r.pdf.RectFromUpperLeftWithStyle(x, y, w, h, "D")
	r.pdf.SetFillColor(255, 0, 0)
	r.setFont("ArialMT", 6)
	r.pdf.SetXY(x+1, y+1)
	r.pdf.Text(label)
}
