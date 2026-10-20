// Package parser 定义了 IDML XML 的解析结构体。
//
// 核心架构：
//   IDML (InDesign Markup) 是一个 ZIP 包，包含以下关键文件：
//   - designmap.xml: 文档元信息（图层、页面引用、字体列表）
//   - Spreads/Spread_*.xml: 页面布局（元素、位置、变换矩阵）
//   - Stories/Story_*.xml: 文本内容及样式
//   - Resources/Graphic.xml: 颜色定义
//   - Resources/Graphics/: 嵌入的素材文件
//
// 坐标系统：
//   IDML 使用 Y-down 坐标系（左上角为原点），与 PDF 的 Y-up 不同。
//   ItemTransform 是行向量格式 [m11 m12 m21 m22 tx ty]，应用方式：
//     x' = m11*x + m21*y + tx
//     y' = m12*x + m22*y + ty
//   这意味着矩阵在概念上是转置的（行主序 vs 列主序）。
package parser

import (
	"encoding/xml"
	"strings"
)

// ============================================================================
// designmap.xml — 文档级元数据
// ============================================================================

// DesignMap 对应 IDML 包根目录下的 designmap.xml。
// 它定义了文档的基本属性、图层层级、页面/故事引用列表和字体信息。
type DesignMap struct {
	XMLName             xml.Name            `xml:"Document"`
	DocumentPreferences DocumentPreferences `xml:"DocumentPreferences"`
	Layers              []Layer             `xml:"Layer"`
	Spreads             []SpreadRef         `xml:"idPkg:Spread"`
	Stories             []StoryRef          `xml:"idPkg:Story"`
	Fonts               []Font              `xml:"Fonts>Font"`
}

// Layer 对应 IDML 的图层。
// 重要：Z 顺序按 designmap.xml 中出现的顺序定义。
//   第一个 <Layer> 在底层（最先绘制），最后一个在顶层（最后绘制）。
type Layer struct {
	Self    string `xml:"Self,attr"`    // 元素唯一 ID（如 "uea"、"u108"）
	Name    string `xml:"Name,attr"`    // 图层名称（如 "图层 1"、"平面图"）
	Visible string `xml:"Visible,attr"` // "true"/"false"，控制图层可见性
}

// DocumentPreferences 包含页面尺寸等文档级别偏好。
type DocumentPreferences struct {
	PageWidth  string `xml:"PageWidth,attr"`  // 页面宽度（点）
	PageHeight string `xml:"PageHeight,attr"` // 页面高度（点）
}

// SpreadRef 是 designmap.xml 中对 Spread 文件的引用。
// Self: 标识符（如 "u1a5"）
// Src: 相对于 Spreads/ 目录的路径（如 "Spread_u102.xml"）
type SpreadRef struct {
	Self string `xml:"Self,attr"`
	Src  string `xml:"src,attr"`
}

// StoryRef 是 designmap.xml 中对 Story 文件的引用。
type StoryRef struct {
	Self string `xml:"Self,attr"`
	Src  string `xml:"src,attr"`
}

// Font 记录文档中使用的字体。
type Font struct {
	FontFamily string `xml:"FontFamily,attr"` // 字族名（如 "SimHei"、"Arial"）
	Name       string `xml:"Name,attr"`       // 完整字体名（如 "SimHei"、"ArialMT"）
}

// ============================================================================
// Spread/*.xml — 页面布局（核心）
// ============================================================================

// Spread 是 Spread XML 的根包装。
// IDML 的 XML 命名空间非常严格，必须正确指定 xmlns。
type Spread struct {
	XMLName xml.Name    `xml:"http://ns.adobe.com/AdobeInDesign/idml/1.0/packaging Spread"`
	Inner   SpreadInner `xml:"Spread"`
}

// SpreadInner 包含实际的 Spread 数据。
// 一个 Spread 可以包含多个 Page（例如对页排版），
// 以及各种类型的元素（矩形、椭圆、多边形、文本框等）。
type SpreadInner struct {
	Self          string     `xml:"Self,attr"`          // 元素唯一 ID
	ItemTransform string     `xml:"ItemTransform,attr"` // Spread 自身的变换矩阵（很少使用）
	Pages         []Page     `xml:"Page"`               // 页面列表（通常 1-2 页）
	Rectangles    []PageItem `xml:"Rectangle"`           // 矩形元素
	Ovals         []PageItem `xml:"Oval"`                // 椭圆元素
	Polygons      []PageItem `xml:"Polygon"`             // 多边形元素
	TextFrames    []PageItem `xml:"TextFrame"`            // 文本框元素
	GraphicLines  []PageItem `xml:"GraphicLine"`          // 线条元素
	EPSTexts      []PageItem `xml:"EPSText"`              // EPS 文本（图形化文字）
	Groups        []Group    `xml:"Group"`               // 编组元素（可递归包含子元素）
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
//
// 组的作用：
//   将多个元素编组后，组有一个统一的 ItemTransform 矩阵，
//   子元素的坐标相对于组内部。在渲染时需要：
//   1. 将组的变换矩阵累积到每个子元素上
//   2. 确保子元素继承组的 ItemLayer
//
// 递归结构：Group 内可以包含 Group，形成嵌套层次结构。
type Group struct {
	Self          string `xml:"Self,attr"`
	ItemTransform string `xml:"ItemTransform,attr"` // 组的变换矩阵
	Visible       string `xml:"Visible,attr"`       // "true"/"false"
	ItemLayer     string `xml:"ItemLayer,attr"`     // 图层 ID

	Rectangles   []PageItem `xml:"Rectangle"`
	Ovals        []PageItem `xml:"Oval"`
	Polygons     []PageItem `xml:"Polygon"`
	TextFrames   []PageItem `xml:"TextFrame"`
	GraphicLines []PageItem `xml:"GraphicLine"`
	EPSTexts     []PageItem `xml:"EPSText"`
	Groups       []Group    `xml:"Group"` // 递归：子组
}

// Page 表示一个页面，包含页面尺寸、变换和母版页信息。
type Page struct {
	Self                string `xml:"Self,attr"`                // 页面唯一 ID
	Name                string `xml:"Name,attr"`                // 页面名称（如 "A"、"1"）
	GeometricBounds     string `xml:"GeometricBounds,attr"`     // 页面边界 "y1 x1 y2 x2"（局部坐标）
	ItemTransform       string `xml:"ItemTransform,attr"`       // 页面变换矩阵（定位到 Spread 中）
	MasterPageTransform string `xml:"MasterPageTransform,attr"` // 母版页偏移矩阵（竖排文字用）
	AppliedMaster       string `xml:"AppliedMaster,attr"`       // 应用的母版页 ID（"n"=无）
	LayoutRule          string `xml:"LayoutRule,attr"`          // "UseMaster"/"Off"
}

// PageItem 统一表示页面上各种元素。
//
// 这是核心类型，Spread 中所有类型的元素（矩形、椭圆、文本框、EPS 文本等）
// 都用此结构体表示。不同元素类型的差异体现在子元素上（Image/PDF/EPSText）。
//
// IDML 元素属性说明：
//   Self: 元素唯一 ID（如 "u11d"），格式为 "u" + 十六进制数字
//   ItemTransform: 6 个浮点数的仿射变换矩阵
//   GeometricBounds: 元素的局部边界框 "y1 x1 y2 x2"
//   ContentType: 内容类型（"GraphicType"=图形、"TextType"=文本、"Unassigned"=未指定）
//   ParentStory: 关联的 Story ID（仅文本框使用）
//   FillColor/StrokeColor: 填充/描边颜色引用（如 "Color/Black"）
//   StrokeWeight: 描边宽度（点）
//   Visible: 可见性
//   ItemLayer: 所在图层 ID（用于 Z 排序）
//
// Properties 子元素：
//   PathGeometry: 路径点数组，定义不规则形状
//   GraphicBounds: 图片/PDF 的裁剪边界
//   PathBoundingBox: 路径的外接矩形
//   EPSTextData: EPS 文本的 Base64 + UTF-16BE 编码数据
//   EPSTextAttributeBounds: EPS 文本的局部边界
//
// Image/PDF/EPSText 子元素：
//   父级元素（Rectangle/TextFrame 等）可包含这些子元素。
//   例如矩形中包含 <Image> 表示这是一个图片框。
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
		EPSTextData             string `xml:"EPSTextData"`
		EPSTextAttributeBounds struct {
			Top    string `xml:"Top,attr"`
			Left   string `xml:"Left,attr"`
			Bottom string `xml:"Bottom,attr"`
			Right  string `xml:"Right,attr"`
		} `xml:"EPSTextAttributeBounds"`
	} `xml:"Properties"`

	Image *Image                            `xml:"Image"`  // 嵌套图片引用
	PDF   *PDF                              `xml:"PDF"`    // 嵌套 PDF/AI 文件引用
	EPSText *EPSText                        `xml:"EPSText"` // EPS 文本元数据

	TextFramePreference *TextFramePreference `xml:"TextFramePreference"` // 文本框属性
}

// PathGeometry 通过路径点定义形状边界。
// 用于不规则形状、自定义路径等。
type PathGeometry struct {
	GeometryPaths []GeometryPathType `xml:"GeometryPathType"`
}

// GeometryPathType 表示一条几何路径。
// PathOpen="" 表示闭合路径（填充区域），PathOpen="true" 表示开放路径。
type GeometryPathType struct {
	PathOpen       string          `xml:"PathOpen,attr"`
	PathPointArray []PathPointType `xml:"PathPointArray>PathPointType"`
}

// PathPointType 表示路径上的一个点，包含锚点和控制点。
// Anchor: 锚点位置 "x y"（局部坐标）
// LeftDirection/RightDirection: 贝塞尔曲线控制点位置
// 对于矩形等基本形状，只有 Anchor 有用。
type PathPointType struct {
	Anchor         string `xml:"Anchor,attr"`
	LeftDirection  string `xml:"LeftDirection,attr"`
	RightDirection string `xml:"RightDirection,attr"`
}

// GraphicBounds 在 PDF/Image 元素中定义源素材的裁剪边界。
// 值表示源素材在 IDML 坐标中的尺寸（点），
// 用于将 Image/PDF 的 ItemTransform 缩放映射到像素坐标。
type GraphicBounds struct {
	Left   string `xml:"Left,attr"`
	Top    string `xml:"Top,attr"`
	Right  string `xml:"Right,attr"`
	Bottom string `xml:"Bottom,attr"`
}

// Image 表示嵌套的图片引用。
//
// 两种使用方式：
// 1. 链接图片：Link 字段非空，通过 LinkResourceURI 引用外部文件
// 2. 内嵌图片：Contents 字段包含 Base64 编码的图片数据（如 TIFF）
//
// ItemTransform 定义了图片相对于父级矩形的变换（缩放/平移），
// 用于图片裁剪蒙版（Rectangle 作为可见窗口，Image 更大然后被裁剪）。
type Image struct {
	Self          string         `xml:"Self,attr"`
	ItemTransform string         `xml:"ItemTransform,attr"` // 图片子变换矩阵
	Link          *Link          `xml:"Link"`               // 外部文件链接
	GraphicBounds *GraphicBounds `xml:"Properties>GraphicBounds"` // 源图片边界
	Contents      string         `xml:"Properties>Contents"`       // 内嵌图片 Base64 数据
}

// PDF 表示嵌套的 PDF/AI 文件引用。
// 结构与 Image 类似，但用于矢量格式（.ai/.pdf）。
type PDF struct {
	Self          string         `xml:"Self,attr"`
	ItemTransform string         `xml:"ItemTransform,attr"`
	Link          *Link          `xml:"Link"`
	GraphicBounds *GraphicBounds `xml:"Properties>GraphicBounds"`
}

// EPSText 表示嵌套的 EPS 文本对象。
// EPS 文本是 InDesign 中"转换为形状"后的文字，
// 不再具有可编辑文本属性，而是作为图形对象存在。
type EPSText struct {
	Self          string `xml:"Self,attr"`
	ItemTransform string `xml:"ItemTransform,attr"`
	FillColor     string `xml:"FillColor,attr"` // 文本颜色
}

// Link 表示外部素材引用。
type Link struct {
	LinkResourceURI string `xml:"LinkResourceURI,attr"` // 素材路径（可能为 file:// URI）
}

// TextFramePreference 存储文本框属性。
// VerticalJustification: "CenterAlign" 等垂直对齐方式。
type TextFramePreference struct {
	VerticalJustification string `xml:"VerticalJustification,attr"`
}

// ============================================================================
// Stories/*.xml — 文本内容
// ============================================================================

// Story 是 Story XML 的根包装。
// Story 包含文档中的一段文本内容，可被多个 TextFrame 引用。
type Story struct {
	XMLName xml.Name   `xml:"http://ns.adobe.com/AdobeInDesign/idml/1.0/packaging Story"`
	Inner   StoryInner `xml:"Story"`
}

// StoryInner 包含实际的 Story 数据。
type StoryInner struct {
	Self       string              `xml:"Self,attr"`       // Story 唯一 ID
	Preference StoryPreference     `xml:"StoryPreference"` // 文本排列方向等属性
	Paragraphs []Paragraph         `xml:"ParagraphStyleRange"` // 段落列表
}

// StoryPreference 描述了文本排列方向。
// StoryOrientation:
//   "" / 空: 水平排列（默认）
//   "Vertical": 竖排文字（每个字符从上到下排列）
type StoryPreference struct {
	StoryOrientation string `xml:"StoryOrientation,attr"`
}

func (s Story) Self() string            { return s.Inner.Self }
func (s Story) Paragraphs() []Paragraph { return s.Inner.Paragraphs }
func (s Story) Vertical() bool          { return s.Inner.Preference.StoryOrientation == "Vertical" }

// Paragraph 表示一个段落，包含字符样式范围。
//
// 关键设计决策：
//   一个 Paragraph 中的多个 CharacterRange 属于同一行，
//   它们只有字体/样式不同，不应换行。
//   换行仅由 <Br /> 元素或 Paragraph 之间的分隔产生。
type Paragraph struct {
	AppliedParagraphStyle string           `xml:"AppliedParagraphStyle,attr"` // 段落样式引用
	Justification         string           `xml:"Justification,attr"`         // 对齐方式
	CharacterRanges       []CharacterRange `xml:"CharacterStyleRange"`        // 字符样式范围列表
	Content               string           `xml:"Content"`                    // 直接文本内容（第一段）
}

// CharacterRange 是一个字符样式范围，包含相同样式的一组字符。
//
// 重要：Go 的 XML 解析器在处理混合内容（Content 和 Br 交替出现）时，
// 只会捕获第一个 Content 和第一个 Br 元素。为处理完整的混合序列，
// 使用 RawInner 字段捕获全部内部 XML，然后由 BuildContent 手动解析。
type CharacterRange struct {
	AppliedCharacterStyle string    `xml:"AppliedCharacterStyle,attr"` // 字符样式引用
	FontStyle             string    `xml:"FontStyle,attr"`             // 字体风格（如 "Bold"）
	PointSize             string    `xml:"PointSize,attr"`            // 字号（点）
	FillColor             string    `xml:"FillColor,attr"`            // 文字颜色引用
	StrokeWeight          string    `xml:"StrokeWeight,attr"`         // 描边粗细
	AppliedLanguage       string    `xml:"AppliedLanguage,attr"`      // 语言
	// Content 和 Br 仅捕获第一个元素（兼容旧代码）
	Content               string    `xml:"Content"`
	Br                    *struct{} `xml:"Br"`
	// RawInner 包含完整的内部 XML，用于提取所有 Content/Br 混合序列。
	// 示例：<Content>一个陈列柜+</Content><Br /><Content>一张桌+灯片</Content>
	RawInner              string    `xml:",innerxml"`
	Properties            struct {
		AppliedFont struct {
			Type  string `xml:"type,attr"`
			Value string `xml:",chardata"` // 字体名称（如 "SimHei"、"Arial"）
		} `xml:"AppliedFont"`
	} `xml:"Properties"`
}

// BuildContent 从 RawInner 中解析所有 Content 和 Br 元素的完整序列。
//
// 为什么需要这个方法：
//   Go 的 xml.Unmarshal 对同一层次多个同名元素支持良好（转为切片），
//   但对 Content 和 Br 交替出现的混合序列支持很差。
//   使用 `xml:",innerxml"` 获取原始 XML 然后手动扫描更可靠。
//
// 扫描逻辑：
//   按顺序查找 <Content> 和 <Br />，遇到 <Content> 提取文本内容，
//   遇到 <Br /> 添加换行符。这种扫描保持了原始文档顺序。
//
// 注意：
//   <Content> 标签是 9 个字符，跳过时使用 ci+9（不是 ci+8）。
//   只有 Br 分支写入 `\n`，Content 分支不会添加额外换行。
//   多个 CharacterRange 属于同一行，BuildContent 对每个 range 独立工作。
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
			// 找到 Content 元素，提取文本
			end := strings.Index(inner[ci:], "</Content>")
			if end < 0 {
				break
			}
			content := inner[ci+9 : ci+end] // 9 = len("<Content>")
			sb.WriteString(content)
			nextStart = ci + end + 10 // 10 = len("</Content>")
		} else {
			// Br 表示换行
			sb.WriteString("\n")
			nextStart = bi + 6 // 6 = len("<Br />") but actually <Br /> is 6 chars
		}
		inner = inner[nextStart:]
	}
	return sb.String()
}