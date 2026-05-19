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
type IDMLDocument struct {
	SourcePath       string
	ExtractDir       string
	zipReader        *zip.ReadCloser
	DesignMap        *DesignMap
	Spreads          []Spread
	Stories          map[string]Story
	EmbeddedGraphics map[string][]byte
	ColorMap         map[string]string // 颜色名 -> CMYK ColorValue
}

// OpenIDML 打开一个 .idml 文件，解压并解析核心 XML。
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

	for _, f := range zr.File {
		targetPath := filepath.Join(extractDir, f.Name)
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
		if strings.HasPrefix(f.Name, "Resources/Graphics/") && !f.FileInfo().IsDir() {
			data, err := os.ReadFile(targetPath)
			if err == nil {
				doc.EmbeddedGraphics[filepath.Base(f.Name)] = data
			}
		}
	}

	dmPath := filepath.Join(extractDir, "designmap.xml")
	if err := doc.parseDesignMap(dmPath); err != nil {
		return nil, fmt.Errorf("parse designmap: %w", err)
	}

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

func (d *IDMLDocument) Close() error {
	if d.zipReader != nil {
		d.zipReader.Close()
	}
	if d.ExtractDir != "" {
		_ = os.RemoveAll(d.ExtractDir)
	}
	return nil
}

func (d *IDMLDocument) parseDesignMap(path string) error {
	var dm DesignMap
	if err := parseXMLFile(path, &dm); err != nil {
		return err
	}
	d.DesignMap = &dm
	return nil
}

func parseXMLFile(path string, v interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return xml.Unmarshal(data, v)
}

// CollectLinkedAssets 收集所有链接素材的 URI。
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
func flattenGroup(g Group, parentTransform TransformMatrix) []PageItem {
	if g.Visible == "false" {
		return nil
	}
	gm, _ := ParseItemTransform(g.ItemTransform)
	globalTransform := parentTransform.Mul(gm)

	var out []PageItem

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

	for _, it := range g.Rectangles {
		out = append(out, process(it))
	}
	for _, it := range g.Ovals {
		out = append(out, process(it))
	}
	for _, it := range g.Polygons {
		out = append(out, process(it))
	}
	for _, it := range g.TextFrames {
		out = append(out, process(it))
	}
	for _, it := range g.GraphicLines {
		out = append(out, process(it))
	}
	for _, it := range g.EPSTexts {
		out = append(out, process(it))
	}
	for _, childGroup := range g.Groups {
		out = append(out, flattenGroup(childGroup, globalTransform)...)
	}
	return out
}

// GetPageBounds 从 Page 的 GeometricBounds 和 ItemTransform 计算页面实际边界。
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

// GetMasterPageYOffset 返回 MasterPageTransform 的 Y 偏移量，用于竖排文字位置修正。
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


// LayerOrder 返回指定图层 ID 的 Z 顺序索引（0=底层）。未找到则返回 999（最上层）。
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
// 同层内按 Self ID 排序以恢复 XML 文档顺序（避免类型分组导致的错误叠放）。
func SortItemsByLayer(items []PageItem, doc *IDMLDocument) {
	type ix struct {
		item   PageItem
		layer  int
		selfID string
		orig   int
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
					// 无 Self 的回收 orig
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
type GraphicColor struct {
	Self        string `xml:"Self,attr"`
	Space       string `xml:"Space,attr"`
	ColorValue  string `xml:"ColorValue,attr"`
}

// parseGraphicColors 解析 Graphic.xml 数据，返回颜色名到 CMYK ColorValue 的映射。
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
