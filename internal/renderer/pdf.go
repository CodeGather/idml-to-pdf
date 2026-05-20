// Package renderer 使用 signintech/gopdf 将解析后的 IDML 数据渲染为 PDF。
package renderer

import (
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/h2non/bimg"
	"github.com/signintech/gopdf"
)

// PDFRenderer 封装了基于 gopdf 的 PDF 生成。
type PDFRenderer struct {
	pdf        gopdf.GoPdf
	pageWidth  float64
	pageHeight float64
	fontLoaded map[string]bool
	ColorMap   map[string]string // 颜色名 -> CMYK ColorValue
}

// NewPDFRenderer 创建新的渲染器。
func NewPDFRenderer() *PDFRenderer {
	return &PDFRenderer{
		pageWidth:  595.28,
		pageHeight: 841.89,
		fontLoaded: make(map[string]bool),
	}
}

// SetPageSize 设置页面尺寸（点）。
func (r *PDFRenderer) SetPageSize(w, h float64) {
	r.pageWidth = w
	r.pageHeight = h
}

// AddPage 添加新页面（使用当前设置的页面尺寸）。
func (r *PDFRenderer) AddPage() {
	r.pdf.AddPageWithOption(gopdf.PageOption{
		PageSize: &gopdf.Rect{W: r.pageWidth, H: r.pageHeight},
	})
}

// Start 初始化 PDF 文档。
func (r *PDFRenderer) Start() {
	r.pdf.Start(gopdf.Config{
		PageSize: *gopdf.PageSizeA4,
	})
	// .ttc 集合字体不被 gopdf 支持，仅加载 .ttf 字体
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

func (r *PDFRenderer) mapFont(idmlFont string) string {
	m := map[string]string{
		"黑体":              "SimHei",
		"Adobe 宋体 Std":    "ArialUnicode",
		"宋体":              "ArialUnicode",
		"SimSun":          "ArialUnicode",
		"微软雅黑":            "微软雅黑",
		"Microsoft YaHei": "微软雅黑",
		"Helvetica":       "ArialMT",
		"Arial":           "ArialMT",
		"ArialMT":         "ArialMT",
		"Times New Roman": "Times",
		"Times":           "Times",
		"Courier New":     "ArialMT",
		"Minion Pro":      "Times",
		"Myriad Pro":      "ArialMT",
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

func (r *PDFRenderer) setFont(fontName string, size float64) {
	name := r.mapFont(fontName)
	if name == "" {
		name = "ArialUnicode"
	}
	if _, ok := r.fontLoaded[name]; !ok {
		// 按优先级回退到已加载的字体
		for _, fallback := range []string{"SimHei", "微软雅黑", "ArialUnicode", "Arial", "Times"} {
			if r.fontLoaded[fallback] {
				name = fallback
				break
			}
		}
	}
	if err := r.pdf.SetFont(name, "", size); err != nil {
		// 如果设置失败，再次回退
		for _, fallback := range []string{"ArialUnicode", "Arial", "Times"} {
			if r.fontLoaded[fallback] {
				_ = r.pdf.SetFont(fallback, "", size)
				break
			}
		}
	}
}

// DrawImage 在指定位置绘制图片（支持 .png/.jpg/.ai/.pdf 自动转换）。
// x, y, w, h 是 AABB（外接矩形）的位置和尺寸。
// 用于无旋转或小幅旋转的普通图片。
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
		if err := convertPDFToPng(tmpFile, pngFile); err != nil {
			return fmt.Errorf("convert pdf/ai to png: %w", err)
		}
		defer os.Remove(pngFile)
		drawFile = pngFile

		// 确保最终 PNG 为白色背景
		opaqueFile := tmpFile + ".opaque.png"
		if err := ensureOpaqueWhiteBackground(drawFile, opaqueFile); err == nil {
			defer os.Remove(opaqueFile)
			drawFile = opaqueFile
		}

		// 裁剪白色背景，拉伸到原始帧尺寸
		_, _, cropErr := cropWhiteBackground(drawFile, drawFile+".cropped.png")
		if cropErr == nil {
			defer os.Remove(drawFile + ".cropped.png")
			drawFile = drawFile + ".cropped.png"
		}
	}

	// 直接绘制（无旋转或小幅旋转）
	_ = r.pdf.Image(drawFile, x, y, &gopdf.Rect{W: w, H: h})
	return nil
}

// DrawRotatedImage 在 AABB 中心绘制旋转后的图片（用于 90°/270° 旋转的底图）。
// cx, cy 是 AABB 中心坐标（旋转中心）。
// w, h 是局部（旋转前）尺寸，angle 是旋转角度。
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
		if err := convertPDFToPng(tmpFile, pngFile); err != nil {
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

	// 围绕 AABB 中心旋转，使用局部宽高绘制
	ox := cx - w/2
	oy := cy - h/2
	r.pdf.Rotate(angle, cx, cy)
	_ = r.pdf.Image(drawFile, ox, oy, &gopdf.Rect{W: w, H: h})
	r.pdf.RotateReset()
	return nil
}

// CropImageToVisibleRegion 将图片裁剪到 Rectangle PathGeometry 可见的区域。
// 使用 libvips (bimg) 处理，原生保留 CMYK 色彩空间，零色差。
func CropImageToVisibleRegion(imgData []byte, pathLx1, pathLy1, pathLx2, pathLy2 float64, imgTx, imgTy, imgSx, imgSy float64, gbRight, gbBottom float64) []byte {
	if imgSx == 0 || imgSy == 0 || gbRight == 0 || gbBottom == 0 {
		return imgData
	}

	// 计算可见区域在源图 GraphicBounds 坐标中的范围
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

	// 用 bimg 获取实际像素尺寸
	meta, err := bimg.NewImage(imgData).Metadata()
	if err != nil || meta.Size.Width == 0 || meta.Size.Height == 0 {
		return imgData
	}
	pixW := meta.Size.Width
	pixH := meta.Size.Height

	// 转换为像素坐标
	pixelX1 := int(srcX1 * float64(pixW) / gbRight)
	pixelY1 := int(srcY1 * float64(pixH) / gbBottom)
	pixelX2 := int(srcX2 * float64(pixW) / gbRight)
	pixelY2 := int(srcY2 * float64(pixH) / gbBottom)

	if pixelX1 < 0 {
		pixelX1 = 0
	}
	if pixelY1 < 0 {
		pixelY1 = 0
	}
	if pixelX2 > pixW {
		pixelX2 = pixW
	}
	if pixelY2 > pixH {
		pixelY2 = pixH
	}
	if pixelX1 >= pixelX2 || pixelY1 >= pixelY2 {
		return imgData
	}

	// 用 bimg 裁剪，保留原始色彩空间 (Interpretation 设为原图空间)
	cropW := pixelX2 - pixelX1
	cropH := pixelY2 - pixelY1
	opts := bimg.Options{
		AreaWidth:  cropW,
		AreaHeight: cropH,
		Top:        pixelY1,
		Left:       pixelX1,
		Quality:    90,
	}
	// 如果是 CMYK 空间，设置 interpretation 保留色彩
	if meta.Space == "cmyk" || meta.Space == "CMYK" {
		opts.Interpretation = bimg.InterpretationCMYK
	}
	cropped, err := bimg.NewImage(imgData).Process(opts)
	if err != nil {
		return imgData
	}
	return cropped
}

// convertPDFToPng 将 PDF/AI 转为 PNG，按平台尝试多个外部工具。
// 支持 macOS(sips)、Windows(ImageMagick/Ghostscript)、Linux(pdftoppm/mutool/ImageMagick)。
func convertPDFToPng(src, dst string) error {
	// 某些工具只认 .pdf 扩展名，.ai 可能被拒绝，先复制为 .pdf
	pdfSrc := src
	if filepath.Ext(strings.ToLower(src)) == ".ai" {
		tmpPDF := src + ".pdf"
		if data, err := os.ReadFile(src); err == nil {
			_ = os.WriteFile(tmpPDF, data, 0644)
			defer os.Remove(tmpPDF)
			pdfSrc = tmpPDF
		}
	}

	const rasterDPI = 600

	// 1. 尝试 mutool（mupdf，跨平台，输出精确）
	// mutool 的输出命名规则：将 -o 指定的扩展名去掉后追加 "1.png"
	if err := exec.Command("mutool", "convert", "-O", "resolution="+strconv.Itoa(rasterDPI), "-o", dst, pdfSrc, "1").Run(); err == nil {
		generated := strings.TrimSuffix(dst, filepath.Ext(dst)) + "1.png"
		if _, err := os.Stat(generated); err == nil {
			_ = os.Rename(generated, dst)
			return nil
		}
	}

	// 2. 尝试 pdftoppm（poppler，跨平台，输出 prefix-1.png）
	outPrefix := dst + "_ppm"
	if err := exec.Command("pdftoppm", "-png", "-f", "1", "-l", "1", "-r", strconv.Itoa(rasterDPI), pdfSrc, outPrefix).Run(); err == nil {
		generated := outPrefix + "-1.png"
		if _, err := os.Stat(generated); err == nil {
			_ = os.Rename(generated, dst)
			_ = os.Remove(outPrefix + "-1.png") // 清理
			return nil
		}
	}

	// 3. 平台特定回退
	switch runtime.GOOS {
	case "darwin":
		// sips 支持 PDF/AI 直接转 PNG
		if err := exec.Command("sips", "-s", "format", "png", src, "--out", dst).Run(); err == nil {
			return nil
		}
	case "windows":
		// ImageMagick（新版本命令为 magick）
		if err := exec.Command("magick", "convert", "-density", strconv.Itoa(rasterDPI), pdfSrc+"[0]", dst).Run(); err == nil {
			return nil
		}
		// 旧版 ImageMagick
		if err := exec.Command("convert", "-density", strconv.Itoa(rasterDPI), pdfSrc+"[0]", dst).Run(); err == nil {
			return nil
		}
		// Ghostscript 64-bit
		if err := exec.Command("gswin64c", "-sDEVICE=pngalpha", "-dFirstPage=1", "-dLastPage=1", "-r"+strconv.Itoa(rasterDPI), "-o", dst, pdfSrc).Run(); err == nil {
			return nil
		}
		// Ghostscript 32-bit
		if err := exec.Command("gswin32c", "-sDEVICE=pngalpha", "-dFirstPage=1", "-dLastPage=1", "-r"+strconv.Itoa(rasterDPI), "-o", dst, pdfSrc).Run(); err == nil {
			return nil
		}
	default: // linux / freebsd 等
		// ImageMagick
		if err := exec.Command("convert", "-density", strconv.Itoa(rasterDPI), pdfSrc+"[0]", dst).Run(); err == nil {
			return nil
		}
		// GraphicsMagick
		if err := exec.Command("gm", "convert", "-density", strconv.Itoa(rasterDPI), pdfSrc+"[0]", dst).Run(); err == nil {
			return nil
		}
	}

	return fmt.Errorf("no available tool to convert PDF/AI to PNG on %s (tried mutool, pdftoppm, sips/magick/gs)", runtime.GOOS)
}

// DrawPlaceholder 绘制缺失素材的占位矩形。
func (r *PDFRenderer) DrawPlaceholder(x, y, w, h float64, label string) {
	r.pdf.SetStrokeColor(255, 0, 0)
	r.pdf.SetLineWidth(1)
	r.pdf.RectFromUpperLeftWithStyle(x, y, w, h, "D")

	r.setFont("Helvetica", 8)
	r.pdf.SetFillColor(255, 0, 0)
	r.pdf.SetXY(x+2, y+h-4)
	r.pdf.Cell(nil, label)
}

// DrawTextFrame 绘制文本框，支持旋转和对齐。
// 支持 \n 换行，逐行绘制。
func (r *PDFRenderer) DrawTextFrame(x, y, w, h float64, text string, fontName string, fontSize float64, angle float64, hAlign string, textColor string) {
	if text == "" {
		return
	}
	r.setFont(fontName, fontSize)
	// 设置文本颜色（默认黑色）
	if textColor != "" && textColor != "Color/None" && textColor != "Swatch/None" {
		c, m, y, k := r.parseColorCMYK(textColor)
		r.pdf.SetFillColorCMYK(c, m, y, k)
	} else {
		r.pdf.SetFillColorCMYK(0, 0, 0, 100)
	}

	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return
	}

	mappedFont := r.mapFont(fontName)

	// 行高（含行距）：参考 PDF 行距约 fontSize*1.2
	lineHeight := fontSize * 1.2

	// 计算整块文本的起始 Y（垂直居中，基线偏移）
	// Python 使用 drawCentredString(0, -fontSize*0.35) 在 Y-up 中把基线放在中心下方 0.35*fontSize 处。
	// 换算到 gopdf 的 Y-down 坐标系，目标基线位置为：cy + fontSize*0.35。
	// 但 gopdf 的 Cell() 将 SetXY 的 Y 视为单元格顶部，实际基线 = ty + typoAscender。
	// 我们的字体（微软雅黑/Arial）typoAscender ≈ 0.728*fontSize，因此需要向上补偿。
	// 多行文本从帧中心开始，第一行居中，后续行依次下移
	// 对偶数行的情况，平均分布在帧中心上下
	blockCenterY := y + h/2
	var startY float64
	n := len(lines)
	if n%2 == 1 {
		// 奇数行：中间行居中
		midIdx := n / 2
		startY = blockCenterY - float64(midIdx)*lineHeight + fontSize*0.35 - fontSize*0.728
	} else {
		// 偶数行：两行居中行平均分布在中心上下
		startY = blockCenterY - float64(n-1)*lineHeight/2 + fontSize*0.35 - fontSize*0.728
	}

	baselineNudge := 0.0
	switch mappedFont {
	case "Arial", "Times":
		baselineNudge = fontSize * 0.16
	case "SimHei", "ArialUnicode", "微软雅黑":
		baselineNudge = fontSize * 0.207
		if fontSize <= 10 {
			baselineNudge = fontSize * 0.17
		}
	}
	if baselineNudge != 0 {
		startY -= baselineNudge
	}

	if angle != 0 {
		r.pdf.Rotate(angle, x+w/2, y+h/2)
	}

	// 逐行绘制
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		textWidth, err := r.pdf.MeasureTextWidth(line)
		if err != nil {
			textWidth = w
		}
		var tx float64
		switch hAlign {
		case "LeftJustified":
			tx = x
		case "RightJustified":
			tx = x + w - textWidth
		default:
			tx = x + (w-textWidth)/2
		}
		ty := startY + float64(i)*lineHeight
		r.pdf.SetXY(tx, ty)
		_ = r.pdf.Cell(nil, line)
	}
	if angle != 0 {
		r.pdf.RotateReset()
	}
}

// DrawTextAt 在指定基线位置绘制单行文本（无旋转、无对齐）。
// x, y 是文本的 baseline 原点（与 InDesign/PDF 坐标一致）。
func (r *PDFRenderer) DrawTextAt(x, y float64, text string, fontName string, fontSize float64, textColor string) {
	if text == "" {
		return
	}
	r.setFont(fontName, fontSize)
	if textColor != "" && textColor != "Color/None" && textColor != "Swatch/None" {
		c, m, y, k := r.parseColorCMYK(textColor)
		r.pdf.SetFillColorCMYK(c, m, y, k)
	} else {
		r.pdf.SetFillColorCMYK(0, 0, 0, 100)
	}
	r.pdf.SetXY(x, y)
	_ = r.pdf.Text(text)
}

// DrawVerticalTextAt 在指定位置绘制垂直排列的文本（从下到上堆叠）。
// 每个字符都会逆时针旋转 90 度，以匹配原稿中竖排文字的朝向。
// x, y 是第一个字符旋转前的 baseline 原点；后续字符沿 y 轴负方向（向上）依次排列。
func (r *PDFRenderer) DrawVerticalTextAt(x, y float64, text string, fontName string, fontSize float64, textColor string) {
	if text == "" {
		return
	}
	r.setFont(fontName, fontSize)
	if textColor != "" && textColor != "Color/None" && textColor != "Swatch/None" {
		c, m, y, k := r.parseColorCMYK(textColor)
		r.pdf.SetFillColorCMYK(c, m, y, k)
	} else {
		r.pdf.SetFillColorCMYK(0, 0, 0, 100)
	}
	// 从第一个字符开始，向上（y 减小方向）绘制
	runes := []rune(text)
	for i, rne := range runes {
		charY := y - float64(i)*fontSize
		r.pdf.Rotate(90, x, charY)
		r.pdf.SetXY(x, charY)
		_ = r.pdf.Text(string(rne))
		r.pdf.RotateReset()
	}
}

// DrawVerticalText 绘制竖排文字，逐字从上到下排列（Y-down 坐标），不旋转字符。
// x, y 是文本框左上角，w/h 用于按 VerticalJustification 居中摆放。
func (r *PDFRenderer) DrawVerticalText(x, y, w, h float64, text string, fontName string, fontSize float64, textColor string) {
	if text == "" {
		return
	}
	r.setFont(fontName, fontSize)
	if textColor != "" && textColor != "Color/None" && textColor != "Swatch/None" {
		c, m, y, k := r.parseColorCMYK(textColor)
		r.pdf.SetFillColorCMYK(c, m, y, k)
	} else {
		r.pdf.SetFillColorCMYK(0, 0, 0, 100)
	}
	runes := []rune(text)
	// 在 frame 内垂直居中
	totalTextH := float64(len(runes)) * fontSize
	startY := y + (h-totalTextH)/2
	// 在 frame 内水平居中（竖排文字每字占一个固定宽度）
	charX := x + (w-fontSize)/2
	if charX < x {
		charX = x
	}
	for i := range runes {
		charY := startY + float64(i)*fontSize
		r.pdf.SetXY(charX, charY)
		_ = r.pdf.Text(string(runes[i]))
	}
}

// DrawRect 绘制矩形（填充/描边）。
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

// DrawRotatedRect 绘制旋转矩形（填充/描边），以 AABB 中心为旋转中心，使用原始尺寸。
func (r *PDFRenderer) DrawRotatedRect(x, y, w, h, origW, origH float64, fillColor, strokeColor string, strokeWidth, angle float64) {
	cx := x + w/2
	cy := y + h/2

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

// DrawPolygon 绘制多边形（填充/描边）。points 为 [x1, y1, x2, y2, ...] 的全局坐标。
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

// Output 保存 PDF。
func (r *PDFRenderer) Output(path string) error {
	return r.pdf.WritePdf(path)
}

// imageSize 返回图片的宽高像素数。
func imageSize(path string) (w, h int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return 0, 0
	}
	b := img.Bounds()
	return b.Dx(), b.Dy()
}

// cropWhiteBackground 裁剪 PNG 图像的白色/近白色边框。
// 返回从原图顶部/左侧裁剪的像素数。
func cropWhiteBackground(src, dst string) (cropLeft, cropTop int, err error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return 0, 0, err
	}

	bounds := img.Bounds()
	minX, minY := bounds.Max.X, bounds.Max.Y
	maxX, maxY := bounds.Min.X, bounds.Min.Y

	// 容差：RGB 均大于 0xF000（约 94% 亮度）视为白色背景
	const whiteThreshold = 0xF000

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r < whiteThreshold || g < whiteThreshold || b < whiteThreshold {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
				if y < minY {
					minY = y
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}

	if minX > maxX || minY > maxY {
		return 0, 0, fmt.Errorf("no non-white content found")
	}

	// 裁剪
	cropped := image.NewRGBA(image.Rect(0, 0, maxX-minX+1, maxY-minY+1))
	draw.Draw(cropped, cropped.Bounds(), img, image.Point{minX, minY}, draw.Src)

	out, err := os.Create(dst)
	if err != nil {
		return 0, 0, err
	}
	defer out.Close()
	if err := png.Encode(out, cropped); err != nil {
		return 0, 0, err
	}
	return minX - bounds.Min.X, minY - bounds.Min.Y, nil
}

// ensureOpaqueWhiteBackground 将 PNG 图像合成到白色背景上，避免透明像素在某些 PDF 阅读器中显示为黑色。
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
	// 先填充白色背景
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			white.Set(x, y, image.White)
		}
	}
	// 将原图合成到白色背景上（保留原图 alpha）
	draw.Draw(white, bounds, img, image.Point{bounds.Min.X, bounds.Min.Y}, draw.Over)

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	return png.Encode(out, white)
}

// parseColorCMYK 解析 IDML 颜色为 CMYK 值（0-100），直接用于 PDF DeviceCMYK 颜色空间。
// 无需任何 RGB 转换 — IDML 储存的 CMYK 值直接传给 PDF 的 k/K 操作符。
func (r *PDFRenderer) parseColorCMYK(idmlColor string) (c, m, y, k uint8) {
	switch idmlColor {
	case "Color/Black":
		return 0, 0, 0, 100
	case "Color/None", "Swatch/None":
		return 0, 0, 0, 0
	}

	// 解析 "Color/C=0 M=100 Y=100 K=0" 格式
	var fc, fm, fy, fk float64
	_, err := fmt.Sscanf(idmlColor, "Color/C=%f M=%f Y=%f K=%f", &fc, &fm, &fy, &fk)
	if err == nil {
		return uint8(fc + 0.5), uint8(fm + 0.5), uint8(fy + 0.5), uint8(fk + 0.5)
	}

	// 从颜色映射表查找自定义颜色（如 "Color/u155" → "0 96 95 0"）
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
