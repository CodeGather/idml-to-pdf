// Package parser 定义了 IDML XML 的解析结构体。
package parser

import (
	"encoding/xml"
	"strings"
)

// ============================================================================
// designmap.xml
// ============================================================================

type DesignMap struct {
	XMLName             xml.Name            `xml:"Document"`
	DocumentPreferences DocumentPreferences `xml:"DocumentPreferences"`
	Layers              []Layer             `xml:"Layer"`
	Spreads             []SpreadRef         `xml:"idPkg:Spread"`
	Stories             []StoryRef          `xml:"idPkg:Story"`
	Fonts               []Font              `xml:"Fonts>Font"`
}

// Layer 对应 IDML 的图层，Z 顺序按 designmap.xml 中出现顺序定义（先出现=底层，后出现=顶层）。
type Layer struct {
	Self    string `xml:"Self,attr"`
	Name    string `xml:"Name,attr"`
	Visible string `xml:"Visible,attr"`
}

type DocumentPreferences struct {
	PageWidth  string `xml:"PageWidth,attr"`
	PageHeight string `xml:"PageHeight,attr"`
}

type SpreadRef struct {
	Self string `xml:"Self,attr"`
	Src  string `xml:"src,attr"`
}

type StoryRef struct {
	Self string `xml:"Self,attr"`
	Src  string `xml:"src,attr"`
}

type Font struct {
	FontFamily string `xml:"FontFamily,attr"`
	Name       string `xml:"Name,attr"`
}

// ============================================================================
// Spread/*.xml
// ============================================================================

// Spread 是 Spread XML 的根包装。
type Spread struct {
	XMLName xml.Name    `xml:"http://ns.adobe.com/AdobeInDesign/idml/1.0/packaging Spread"`
	Inner   SpreadInner `xml:"Spread"`
}

// SpreadInner 包含实际的 Spread 数据。
type SpreadInner struct {
	Self          string     `xml:"Self,attr"`
	ItemTransform string     `xml:"ItemTransform,attr"`
	Pages         []Page     `xml:"Page"`
	Rectangles    []PageItem `xml:"Rectangle"`
	Ovals         []PageItem `xml:"Oval"`
	Polygons      []PageItem `xml:"Polygon"`
	TextFrames    []PageItem `xml:"TextFrame"`
	GraphicLines  []PageItem `xml:"GraphicLine"`
	EPSTexts      []PageItem `xml:"EPSText"`
	Groups        []Group    `xml:"Group"`
}

// Convenience accessors (delegated to Inner)
func (s Spread) Self() string             { return s.Inner.Self }
func (s Spread) ItemTransform() string    { return s.Inner.ItemTransform }
func (s Spread) Pages() []Page            { return s.Inner.Pages }
func (s Spread) Rectangles() []PageItem   { return s.Inner.Rectangles }
func (s Spread) Ovals() []PageItem        { return s.Inner.Ovals }
func (s Spread) Polygons() []PageItem     { return s.Inner.Polygons }
func (s Spread) TextFrames() []PageItem   { return s.Inner.TextFrames }
func (s Spread) GraphicLines() []PageItem { return s.Inner.GraphicLines }
func (s Spread) EPSTexts() []PageItem     { return s.Inner.EPSTexts }
func (s Spread) Groups() []Group          { return s.Inner.Groups }

// Group 表示 InDesign 中的编组，可递归包含子元素。
type Group struct {
	Self          string `xml:"Self,attr"`
	ItemTransform string `xml:"ItemTransform,attr"`
	Visible       string `xml:"Visible,attr"`
	ItemLayer     string `xml:"ItemLayer,attr"`

	Rectangles   []PageItem `xml:"Rectangle"`
	Ovals        []PageItem `xml:"Oval"`
	Polygons     []PageItem `xml:"Polygon"`
	TextFrames   []PageItem `xml:"TextFrame"`
	GraphicLines []PageItem `xml:"GraphicLine"`
	EPSTexts     []PageItem `xml:"EPSText"`
	Groups       []Group    `xml:"Group"`
}

type Page struct {
	Self                string `xml:"Self,attr"`
	Name                string `xml:"Name,attr"`
	GeometricBounds     string `xml:"GeometricBounds,attr"`
	ItemTransform       string `xml:"ItemTransform,attr"`
	MasterPageTransform string `xml:"MasterPageTransform,attr"`
	AppliedMaster       string `xml:"AppliedMaster,attr"`
}

// PageItem 统一表示页面上各种元素。
type PageItem struct {
	Self            string `xml:"Self,attr"`
	ItemTransform   string `xml:"ItemTransform,attr"`
	GeometricBounds string `xml:"GeometricBounds,attr"`
	ContentType     string `xml:"ContentType,attr"`
	ParentStory     string `xml:"ParentStory,attr"`
	FillColor       string `xml:"FillColor,attr"`
	StrokeColor     string `xml:"StrokeColor,attr"`
	StrokeWeight    string `xml:"StrokeWeight,attr"`
	Visible         string `xml:"Visible,attr"`
	ItemLayer       string `xml:"ItemLayer,attr"`

	Properties struct {
		PathGeometry  *PathGeometry  `xml:"PathGeometry"`
		GraphicBounds *GraphicBounds `xml:"GraphicBounds"`
		PathBoundingBox struct {
			Left   string `xml:"Left,attr"`
			Top    string `xml:"Top,attr"`
			Right  string `xml:"Right,attr"`
			Bottom string `xml:"Bottom,attr"`
		} `xml:"PathBoundingBox"`
		EPSTextData string `xml:"EPSTextData"`
		EPSTextAttributeBounds struct {
			Top    string `xml:"Top,attr"`
			Left   string `xml:"Left,attr"`
			Bottom string `xml:"Bottom,attr"`
			Right  string `xml:"Right,attr"`
		} `xml:"EPSTextAttributeBounds"`
	} `xml:"Properties"`

	Image *Image `xml:"Image"`
	PDF   *PDF   `xml:"PDF"`
	EPSText *EPSText `xml:"EPSText"`

	TextFramePreference *TextFramePreference `xml:"TextFramePreference"`
}

// PathGeometry 通过路径点定义形状边界。
type PathGeometry struct {
	GeometryPaths []GeometryPathType `xml:"GeometryPathType"`
}

// GeometryPathType 表示一条几何路径。
type GeometryPathType struct {
	PathOpen       string          `xml:"PathOpen,attr"`
	PathPointArray []PathPointType `xml:"PathPointArray>PathPointType"`
}

// PathPointType 表示路径上的一个点。
type PathPointType struct {
	Anchor         string `xml:"Anchor,attr"`
	LeftDirection  string `xml:"LeftDirection,attr"`
	RightDirection string `xml:"RightDirection,attr"`
}

// GraphicBounds 在 PDF 元素中用于定义图片裁剪边界。
type GraphicBounds struct {
	Left   string `xml:"Left,attr"`
	Top    string `xml:"Top,attr"`
	Right  string `xml:"Right,attr"`
	Bottom string `xml:"Bottom,attr"`
}

// Image 表示嵌套的图片引用。
type Image struct {
	Self          string `xml:"Self,attr"`
	ItemTransform string `xml:"ItemTransform,attr"`
	Link          *Link  `xml:"Link"`
}

// PDF 表示嵌套的 PDF/AI 文件引用。
type PDF struct {
	Self          string         `xml:"Self,attr"`
	ItemTransform string         `xml:"ItemTransform,attr"`
	Link          *Link          `xml:"Link"`
	GraphicBounds *GraphicBounds `xml:"Properties>GraphicBounds"`
}

// EPSText 表示嵌套的 EPS 文本对象。
type EPSText struct {
	Self          string `xml:"Self,attr"`
	ItemTransform string `xml:"ItemTransform,attr"`
	FillColor     string `xml:"FillColor,attr"`
}

// Link 表示外部素材引用。
type Link struct {
	LinkResourceURI string `xml:"LinkResourceURI,attr"`
}

// TextFramePreference 存储文本框属性（列数、竖排对齐等）。
type TextFramePreference struct {
	VerticalJustification string `xml:"VerticalJustification,attr"`
}

// ============================================================================
// Stories/*.xml
// ============================================================================

// Story 是 Story XML 的根包装。
type Story struct {
	XMLName xml.Name   `xml:"http://ns.adobe.com/AdobeInDesign/idml/1.0/packaging Story"`
	Inner   StoryInner `xml:"Story"`
}

// StoryInner 包含实际的 Story 数据。
type StoryInner struct {
	Self       string              `xml:"Self,attr"`
	Preference StoryPreference     `xml:"StoryPreference"`
	Paragraphs []Paragraph         `xml:"ParagraphStyleRange"`
}

// StoryPreference 描述了文本排列方向。
type StoryPreference struct {
	StoryOrientation string `xml:"StoryOrientation,attr"`
}

func (s Story) Self() string            { return s.Inner.Self }
func (s Story) Paragraphs() []Paragraph { return s.Inner.Paragraphs }
func (s Story) Vertical() bool          { return s.Inner.Preference.StoryOrientation == "Vertical" }

type Paragraph struct {
	AppliedParagraphStyle string           `xml:"AppliedParagraphStyle,attr"`
	Justification         string           `xml:"Justification,attr"`
	CharacterRanges       []CharacterRange `xml:"CharacterStyleRange"`
	Content               string           `xml:"Content"`
}

type CharacterRange struct {
	AppliedCharacterStyle string    `xml:"AppliedCharacterStyle,attr"`
	FontStyle             string    `xml:"FontStyle,attr"`
	PointSize             string    `xml:"PointSize,attr"`
	FillColor             string    `xml:"FillColor,attr"`
	StrokeWeight          string    `xml:"StrokeWeight,attr"`
	AppliedLanguage       string    `xml:"AppliedLanguage,attr"`
	// Content 和 Br 仅捕获第一个元素（兼容旧代码）
	Content               string    `xml:"Content"`
	Br                    *struct{} `xml:"Br"`
	// RawInner 包含完整的内 XML，用于提取所有 Content/Br 混合序列
	RawInner              string    `xml:",innerxml"`
	Properties            struct {
		AppliedFont struct {
			Type  string `xml:"type,attr"`
			Value string `xml:",chardata"`
		} `xml:"AppliedFont"`
	} `xml:"Properties"`
}

// BuildContent 从 RawInner 中解析所有 Content 和 Br 元素的完整序列。
func (cr CharacterRange) BuildContent() string {
	if cr.RawInner == "" {
		return cr.Content
	}
	var sb strings.Builder
	inner := cr.RawInner
	for {
		ci := strings.Index(inner, "<Content>")
		bi := strings.Index(inner, "<Br />")
		if ci < 0 && bi < 0 {
			break
		}
		var nextStart int
		if ci >= 0 && (bi < 0 || ci < bi) {
			end := strings.Index(inner[ci:], "</Content>")
			if end < 0 {
				break
			}
			content := inner[ci+9 : ci+end]
			sb.WriteString(content)
			nextStart = ci + end + 10
		} else {
			// Br 表示换行
			sb.WriteString("\n")
			nextStart = bi + 6
		}
		inner = inner[nextStart:]
	}
	return sb.String()
}