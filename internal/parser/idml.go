// Package parser — IDML 文件解析与元素排列
//
// 该文件负责：
// 1. 解压 IDML ZIP 包并提取所有文件
// 2. 解析 designmap.xml、Spread XML、Story XML
// 3. 从 Resources/Graphic.xml 建立颜色映射
// 4. 展平编组（Group）并维护图层 Z 顺序
// 5. 获取母版页 Y 偏移（竖排文字专用）
//
// 整体流程：
//   OpenIDML → 解压 ZIP → 解析 designmap → 解析 Spreads → 解析 Stories
//   → 解析颜色 → 返回 IDMLDocument
//   → 后续用 FlattenPageItems + SortItemsByLayer 处理元素
package parser

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// IDMLDocument 表示解压后的 IDML 内容集合。
// 这是解析阶段的输出，也是渲染阶段的输入。
type IDMLDocument struct {
	SourcePath       string                   // 原始 .idml 文件路径
	ExtractDir       string                   // 解压后的临时目录
	zipReader        *zip.ReadCloser           // ZIP 读取器（用于 Close 时关闭）
	DesignMap        *DesignMap                // 文档元数据
	Spreads          []Spread                  // 页面布局列表
	Stories          map[string]Story           // 文本内容（key = Story Self ID）
	EmbeddedGraphics map[string][]byte          // 嵌入素材（key = 文件名，如 "graphic_001.jpg"）
	ColorMap         map[string]string          // 颜色名 → CMYK ColorValue
}

// OpenIDML 打开一个 .idml 文件，解压并解析核心 XML。
//
// 参数：
//   idmlPath: .idml 文件路径
//   extractDir: 解压目标目录（空字符串表示创建临时目录）
//   keepExtracted: 是否保留解压后的文件（调试用）
//
// 返回：
//   IDMLDocument: 包含所有解析后的数据
//
// 解压后解析的 XML 文件：
//   1. designmap.xml — 文档结构元数据
//   2. Spreads/*.xml — 页面布局（每个文件一个 Spread）
//   3. Stories/*.xml — 文本内容
//   4. Resources/Graphic.xml — 颜色定义
//   5. Resources/Graphics/* — 嵌入素材（二进制文件）
func OpenIDML(idmlPath, extractDir string, keepExtracted bool) (*IDMLDocument, error) {
	zr, err := zip.OpenReader(idmlPath)
	if err != nil {
		return nil, fmt.Errorf("open idml zip: %w", err)
	}

	if extractDir == "" {
		extractDir, err = os.MkdirTemp("", "idml-extract-*")
		if err != nil {
			return nil, fmt.Errorf("create temp dir: %w", err)
		}
	}

	doc := &IDMLDocument{
		SourcePath:       idmlPath,
		ExtractDir:       extractDir,
		zipReader:        zr,
		Stories:          make(map[string]Story),
		EmbeddedGraphics: make(map[string][]byte),
	}

	// 解压所有 ZIP 条目
	for _, f := range zr.File {
		targetPath := filepath.Join(extractDir, f.Name)
		// 防止 ZIP 路径穿越攻击
		if !strings.HasPrefix(filepath.Clean(targetPath), filepath.Clean(extractDir)+string(os.PathSeparator)) {
			return nil, fmt.Errorf("illegal zip entry: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(targetPath, 0755)
			continue
		}
		os.MkdirAll(filepath.Dir(targetPath), 0755)
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open zip entry %s: %w", f.Name, err)
		}
		out, err := os.Create(targetPath)
		if err != nil {
			rc.Close()
			return nil, fmt.Errorf("create extracted file %s: %w", targetPath, err)
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("extract %s: %w", f.Name, err)
		}
		// 收集嵌入素材文件（Resources/Graphics/ 目录下的文件）
		if strings.HasPrefix(f.Name, "Resources/Graphics/") && !f.FileInfo().IsDir() {
			data, err := os.ReadFile(targetPath)
			if err == nil {
				doc.EmbeddedGraphics[filepath.Base(f.Name)] = data
			}
		}
	}

	// 解析设计映射
	dmPath := filepath.Join(extractDir, "designmap.xml")
	if err := doc.parseDesignMap(dmPath); err != nil {
		return nil, fmt.Errorf("parse designmap: %w", err)
	}

	// 解析所有 Spread XML
	spreadsDir := filepath.Join(extractDir, "Spreads")
	entries, err := os.ReadDir(spreadsDir)
	if err == nil {
		for _, ent := range entries {
			if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".xml") {
				continue
			}
			var sp Spread
			if err := parseXMLFile(filepath.Join(spreadsDir, ent.Name()), &sp); err == nil {
				doc.Spreads = append(doc.Spreads, sp)
			}
		}
	}

	// 解析所有 Story XML
	storiesDir := filepath.Join(extractDir, "Stories")
	entries, err = os.ReadDir(storiesDir)
	if err == nil {
		for _, ent := range entries {
			if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".xml") {
				continue
			}
			var st Story
			if err := parseXMLFile(filepath.Join(storiesDir, ent.Name()), &st); err == nil {
				doc.Stories[st.Self()] = st
			}
		}
	}

	// 解析 Resources/Graphic.xml 建立颜色映射
	graphicPath := filepath.Join(extractDir, "Resources", "Graphic.xml")
	if data, err := os.ReadFile(graphicPath); err == nil {
		doc.ColorMap = parseGraphicColors(data)
	}

	return doc, nil
}

// Close 清理资源：关闭 ZIP 文件、删除解压目录。
func (d *IDMLDocument) Close() error {
	if d.zipReader != nil {
		d.zipReader.Close()
	}
	if d.ExtractDir != "" {
		_ = os.RemoveAll(d.ExtractDir)
	}
	return nil
}

// parseDesignMap 解析 designmap.xml。
func (d *IDMLDocument) parseDesignMap(path string) error {
	var dm DesignMap
	if err := parseXMLFile(path, &dm); err != nil {
		return err
	}
	d.DesignMap = &dm
	return nil
}

// parseXMLFile 通用的 XML 文件解析器。
func parseXMLFile(path string, v interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return xml.Unmarshal(data, v)
}

// CollectLinkedAssets 收集所有链接素材的 URI。
// 遍历所有 Spread 中的所有元素，收集 Image 和 PDF 子元素的 Link URI。
// 用于输出调试信息和检查素材完整性。
func (d *IDMLDocument) CollectLinkedAssets() []string {
	seen := make(map[string]struct{})
	var uris []string
	for _, sp := range d.Spreads {
		items := FlattenPageItems(sp)
		for _, it := range items {
			var uri string
			if it.Image != nil && it.Image.Link != nil {
				uri = it.Image.Link.LinkResourceURI
			}
			if it.PDF != nil && it.PDF.Link != nil {
				uri = it.PDF.Link.LinkResourceURI
			}
			if uri == "" {
				continue
			}
			if _, ok := seen[uri]; !ok {
				seen[uri] = struct{}{}
				uris = append(uris, uri)
			}
		}
	}
	return uris
}

// FlattenPageItems 将 Spread 中所有类型的元素（含递归展开 Group）展平为统一列表。
//
// 注意：此处按类型分组收集（全部 Rectangle、然后全部 Oval...），
// 这会打乱 XML 文档中的原始顺序。
// 必须在返回后调用 SortItemsByLayer 恢复正确的 Z 顺序。
func FlattenPageItems(sp Spread) []PageItem {
	var out []PageItem
	out = append(out, sp.Inner.Rectangles...)
	out = append(out, sp.Inner.Ovals...)
	out = append(out, sp.Inner.Polygons...)
	out = append(out, sp.Inner.TextFrames...)
	out = append(out, sp.Inner.GraphicLines...)
	out = append(out, sp.Inner.EPSTexts...)
	for _, g := range sp.Inner.Groups {
		out = append(out, flattenGroup(g, Translate(0, 0))...)
	}
	return out
}

// flattenGroup 递归展开 Group，将父级变换矩阵累积到子元素。
//
// 关键操作：
// 1. 组的变换矩阵与子元素的变换矩阵相乘（组合）
// 2. 组内子元素继承组的 ItemLayer（如果子元素本身没设置）
//
// 组嵌套示例：
//   最外层组: transform = T1, layer = "u10f"
//     ├─ 子矩形: transform = T2 → 合并后 = T1·T2, layer = "u10f"
//     └─ 子组: transform = T3
//          └─ 子矩形: transform = T4 → 合并后 = T1·T3·T4, layer = "u10f"
func flattenGroup(g Group, parentTransform TransformMatrix) []PageItem {
	if g.Visible == "false" {
		return nil
	}
	gm, _ := ParseItemTransform(g.ItemTransform)
	globalTransform := parentTransform.Mul(gm)

	var out []PageItem

	// process 函数将组的变换累积到单个子元素上
	process := func(it PageItem) PageItem {
		cm, _ := ParseItemTransform(it.ItemTransform)
		combined := globalTransform.Mul(cm)
		it.ItemTransform = fmt.Sprintf("%g %g %g %g %g %g",
			combined.M11, combined.M12, combined.M21, combined.M22, combined.Tx, combined.Ty)
		// 组内元素继承组的 ItemLayer（如果自身没设置）
		if it.ItemLayer == "" && g.ItemLayer != "" {
			it.ItemLayer = g.ItemLayer
		}
		return it
	}

	for _, it := range g.Rectangles   { out = append(out, process(it)) }
	for _, it := range g.Ovals        { out = append(out, process(it)) }
	for _, it := range g.Polygons     { out = append(out, process(it)) }
	for _, it := range g.TextFrames   { out = append(out, process(it)) }
	for _, it := range g.GraphicLines { out = append(out, process(it)) }
	for _, it := range g.EPSTexts     { out = append(out, process(it)) }
	for _, childGroup := range g.Groups {
		out = append(out, flattenGroup(childGroup, globalTransform)...)
	}
	return out
}

// GetPageBounds 从 Page 的 GeometricBounds 和 ItemTransform 计算页面实际边界。
//
// 返回值：页面左上角在 Spread 坐标中的位置 (x, y) 以及页面宽高 (w, h)。
// 这些值用于将元素从 Spread 坐标转换为页面坐标。
//
// 注意：这里不使用 MasterPageTransform，因为该偏移只影响竖排文字。
func GetPageBounds(p Page) (x, y, w, h float64, err error) {
	b, err := ParseGeometricBounds(p.GeometricBounds)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	m, err := ParseItemTransform(p.ItemTransform)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	x1, y1 := m.Apply(b.X1, b.Y1)
	return x1, y1, b.Width(), b.Height(), nil
}

// GetMasterPageYOffset 返回 MasterPageTransform 的 Y 偏移量。
//
// 使用场景：
//   当 Page 的 LayoutRule="UseMaster" 时，页面应用了母版页偏移。
//   水平元素的 ItemTransform 已经包含了这个偏移，但竖排文字
//   的坐标计算路径不同（使用 Story 坐标单独计算），需要额外
//   加上此偏移才能使竖排文字与参考 PDF 对齐。
//
// 注意：不要对水平元素应用此偏移，否则会产生双重偏移。
func GetMasterPageYOffset(p Page) float64 {
	if p.AppliedMaster == "" || p.AppliedMaster == "n" || p.MasterPageTransform == "" {
		return 0
	}
	mm, err := ParseItemTransform(p.MasterPageTransform)
	if err != nil {
		return 0
	}
	return mm.Ty
}

// LayerOrder 返回指定图层 ID 的 Z 顺序索引（0=底层）。
// 如果找不到图层，返回 999（确保显示在最上层）。
func (d *IDMLDocument) LayerOrder(layerID string) int {
	if d.DesignMap == nil {
		return 0
	}
	for i, l := range d.DesignMap.Layers {
		if l.Self == layerID {
			return i
		}
	}
	return 999
}

// SortItemsByLayer 按图层 Z 顺序对 PageItem 列表排序（底层在前）。
//
// 为何需要排序：
//   FlattenPageItems() 按类型分组收集元素，打乱了 XML 文档顺序。
//   InDesign 的 Z 顺序由两个因素决定：
//   1. 图层顺序（designmap.xml 中 <Layer> 的出现顺序）
//   2. 同层内元素顺序（XML 文档中的出现顺序）
//
// 排序策略：
//   主键 = 图层顺序（LayerOrder），次键 = Self ID（文档顺序）
//   Self ID 格式为 "u" + 十六进制数字，字符串比较在同等长度下
//   等价于数值比较。
//
// 注意：如果 Self ID 长度不一致，字符串比较会出错。
// 当前所有 ID 都是相同长度，所以可以安全使用。
func SortItemsByLayer(items []PageItem, doc *IDMLDocument) {
	type ix struct {
		item   PageItem
		layer  int
		selfID string
		orig   int // 原始序号（当 selfID 不可用时回退）
	}

	sorted := make([]ix, len(items))
	for i, it := range items {
		l := 0
		if it.ItemLayer != "" {
			l = doc.LayerOrder(it.ItemLayer)
		}
		// 解析 Self ID：格式 "u" + hex，直接按字符串比较（长度一致时字符串序 = 数值序）
		selfID := it.Self
		if len(selfID) > 1 && selfID[0] == 'u' {
			selfID = selfID[1:]
		} else {
			selfID = ""
		}
		sorted[i] = ix{it, l, selfID, i}
	}

	// 先按图层排序，同层按 Self ID（回收 orig 保底）
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].layer < sorted[i].layer {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			} else if sorted[j].layer == sorted[i].layer {
				// 同层：优先按 Self ID 排序
				if sorted[j].selfID != "" && sorted[i].selfID != "" && sorted[j].selfID < sorted[i].selfID {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				} else if sorted[j].selfID == "" && sorted[j].orig < sorted[i].orig {
					// 无 Self 的回退 orig
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
			}
		}
	}

	for i := range sorted {
		items[i] = sorted[i].item
	}
}

// GraphicXML 表示 Resources/Graphic.xml 的根结构。
type GraphicXML struct {
	Colors []GraphicColor `xml:"Color"`
}

// GraphicColor 表示 Graphic.xml 中的颜色定义。
// Self: 颜色引用名（如 "Color/C=0 M=100 Y=100 K=0"、"Color/u155"）
// Space: 色彩空间（"CMYK" 或 "RGB"）
// ColorValue: 颜色值（如 "0 100 100 0" 或 "255 255 255"）
type GraphicColor struct {
	Self        string `xml:"Self,attr"`
	Space       string `xml:"Space,attr"`
	ColorValue  string `xml:"ColorValue,attr"`
}

// parseGraphicColors 解析 Graphic.xml 数据，返回颜色名到 CMYK ColorValue 的映射。
//
// 映射格式：
//   key: 颜色 Self 属性（如 "Color/C=0 M=100 Y=100 K=0" 或 "Color/u155"）
//   value: 颜色 ColorValue 属性（如 "0 100 100 0"）
//
// 注意：ColorValue 是原始字符串，包含浮点数（如 "7.000000029802322 93.99999976158142 ..."）
// 在渲染时通过 parseColorCMYK 解析为整数 CMYK 值。
func parseGraphicColors(data []byte) map[string]string {
	m := make(map[string]string)
	var g GraphicXML
	if err := xml.Unmarshal(data, &g); err != nil {
		return m
	}
	for _, c := range g.Colors {
		if c.Self != "" && c.ColorValue != "" {
			m[c.Self] = c.ColorValue
		}
	}
	return m
}