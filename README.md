# IDML 转 PDF 技术方案（Go 语言实现）

本文档提供了一套完整的、可直接落地的 Go 语言技术方案，用于将 Adobe InDesign IDML 文件解析并渲染为 PDF。方案覆盖了 IDML 结构解析、素材回退加载、坐标变换、PDF 生成等核心环节，并附带了可直接编译运行的示例代码。

---

## 1. IDML 文件结构解析

### 1.1 IDML 的本质

IDML（InDesign Markup Language）是 Adobe InDesign 的文档交换格式，其物理形态是一个标准的 ZIP 压缩包，扩展名为 `.idml`。解压后，内部包含一组按目录组织的 XML 文件和资源文件。所有排版信息、页面结构、文本内容、样式定义均以 XML 形式存储，而图片等媒体资源既可以嵌入在包内，也可以以外部链接的形式存在。

解压后的典型目录结构如下：

```
sample.idml/
├── META-INF/
│   └── container.xml
├── mimetype
├── designmap.xml              <-- 文档根索引，定义页面尺寸、Spread/Story 引用、字体列表
├── MasterSpreads/             <-- 主页跨页
│   └── MasterSpread_uc0.xml
├── Spreads/                   <-- 普通跨页（核心排版数据）
│   ├── Spread_ub6.xml
│   └── Spread_uc1.xml
├── Stories/                   <-- 文本流（与文本框通过 ParentStory 关联）
│   ├── Story_u1fb.xml
│   └── Story_u20c.xml
├── Resources/                 <-- 资源定义
│   ├── Fonts.xml              <-- 字体元数据
│   ├── Graphic.xml            <-- 嵌入图片的元数据索引
│   ├── Preferences.xml
│   └── Styles.xml             <-- 段落样式、字符样式
└── Resources/Graphics/        <-- 实际嵌入的图片二进制文件
    ├── img001.jpg
    └── img002.png
```

### 1.2 核心文件与目录说明

| 路径 | 作用 | 与素材路径的关联 |
|------|------|-----------------|
| `designmap.xml` | 文档全局描述：页面尺寸（`DocumentPreferences`）、所有 `SpreadRef` 和 `StoryRef` 的入口、字体列表（`Fonts::Font`） | 定义文档的物理尺寸，决定 PDF 的页面大小 |
| `Spreads/*.xml` | 每个 Spread 包含若干 `Page` 和 `PageItem`。`PageItem` 是页面元素（文本框、图片框、矩形、线条等）的容器 | 图片框内嵌套 `<Image>` → `<Link>` 节点，`LinkResourceURI` 属性指向素材路径 |
| `Resources/Graphics/` | 存放实际嵌入的图片二进制文件 | 当 `Link::StoredState="Embedded"` 时，图片数据在此目录下，文件名为 `LinkResourceURI` 指向的值 |
| `Resources/Links/` | （旧版 IDML 可能出现）外部链接的索引 | 新版 IDML 通常直接在 `Spread` 的 `Link` 节点中维护 URI |
| `Stories/*.xml` | 纯文本内容与样式信息，通过 `id` 与 `Spread` 中的 `TextFrame`（`ParentStory` 属性）关联 | 不包含素材路径，但包含字体名（`FontStyle`、`AppliedCharacterStyle`），与字体映射相关 |

### 1.3 嵌入素材与链接素材的区分

在 `Spread` 的 `PageItem` → `Image` → `Link` 节点中，通过 `StoredState` 属性判断素材的存储方式：

```xml
<!-- 嵌入素材示例：StoredState="Embedded"，LinkResourceURI 仅为文件名 -->
<Link Self="u12b" LinkResourceURI="img001.jpg" StoredState="Embedded" />

<!-- 链接素材示例：StoredState="Normal"，LinkResourceURI 为外部路径 -->
<Link Self="u12c" LinkResourceURI="file:///Users/design/assets/logo.png" StoredState="Normal" />
```

**判断逻辑**：

1. 读取 `Link.StoredState`。
2. 若为 `"Embedded"`，到 `Resources/Graphics/` 下查找 `LinkResourceURI` 指定的文件名，读取二进制内容。
3. 若为 `"Normal"` 或缺失，将 `LinkResourceURI` 视为外部路径进行解析和加载。

---

## 2. 素材路径提取与处理策略

### 2.1 路径提取节点

素材路径的完整提取链路为：

```
Spread.xml
  └── PageItem (ContentType="GraphicType" 或包含 <Image>)
        └── Image
              └── Link
                    └── @LinkResourceURI   <-- 目标属性
```

代码中对应的结构体链路为：`Spread` → `PageItem` → `Image` → `Link` → `LinkResourceURI`。

### 2.2 URI 格式处理

IDML 中 `LinkResourceURI` 可能出现的格式包括：

| 格式示例 | 处理方式 |
|---------|---------|
| `file:///Users/foo/assets/img.jpg` | 去掉 `file://` 前缀，得到绝对路径 `/Users/foo/assets/img.jpg` |
| `file:///C:/project/assets/img.jpg` | Windows 路径，去掉 `file:///` 后得到 `C:/project/assets/img.jpg` |
| `file:assets/img.jpg` | 相对路径，去掉 `file:` 后按相对路径处理 |
| `assets/img.jpg` | 纯相对路径，基于当前工作目录或用户指定的素材根目录解析 |
| `img001.jpg` | 仅文件名，视为嵌入素材或在根目录下查找 |

**清洗函数核心逻辑**（见 `internal/assets/loader.go` 的 `cleanURI`）：

```go
func cleanURI(uri string) string {
    s := uri
    if strings.HasPrefix(s, "file://") {
        s = s[len("file://"):]
        if len(s) >= 2 && s[0] == '/' && s[2] == ':' {
            s = s[1:] // Windows 路径修正
        }
    } else if strings.HasPrefix(s, "file:") {
        s = s[len("file:"):]
    }
    return filepath.Clean(s)
}
```

### 2.3 多级回退查找机制

当原始路径不可访问时，加载器按以下优先级查找：

```
优先级 1: 嵌入素材表（Resources/Graphics/ 下的文件）
优先级 2: 清洗后的原始路径（绝对路径或相对于 CWD）
优先级 3: 用户指定的素材根目录 + 清洗后的路径
优先级 4: 用户指定的素材根目录 + 仅文件名
优先级 5: 在素材根目录下递归搜索同名文件（FallbackSearch）
优先级 6: 生成占位图并记录日志
```

实现要点：

- `AssetRoots` 支持多个根目录，按顺序匹配。
- 递归搜索使用 `filepath.Walk`，通过 `io.EOF` 技巧提前终止。
- 所有回退事件通过 `Logger` 记录，便于后续排查。

### 2.4 缺失素材的占位处理

在最后一级回退中，如果配置了 `PlaceholderGenerator`，会在 PDF 中绘制一个带红色边框和文件名的占位矩形，同时日志输出 `素材缺失: [文件名]`。这样既保证了 PDF 生成不中断，又明确标示了缺失内容的位置。

---

## 3. Go 代码架构设计

### 3.1 推荐项目目录结构

```
idml-to-pdf/
├── go.mod
├── main.go                      <-- 简化示例入口
├── cmd/
│   └── converter/
│       └── main.go              <-- 生产级 CLI（可扩展参数和并发处理）
├── internal/
│   ├── parser/
│   │   ├── idml.go              <-- ZIP 解压、XML 解析 orchestration
│   │   ├── xml_types.go         <-- IDML XML 的 Go 结构体映射
│   │   └── transform.go         <-- ItemTransform / GeometricBounds 解析
│   ├── assets/
│   │   └── loader.go            <-- 素材路径解析、多级回退、嵌入/链接区分
│   └── renderer/
│       └── pdf.go               <-- 基于 gofpdf 的渲染器
├── pkg/                         <-- 如有需要对外暴露的公共接口
│   └── converter/
│       └── converter.go
└── test/
    ├── sample.idml
    └── assets/
```

### 3.2 核心接口定义

虽然 Go 语言惯用具体类型而非接口，但在模块边界处定义接口有助于单元测试和模块解耦：

```go
// IDMLParser 负责从 ZIP/XML 中解析出文档对象模型。
type IDMLParser interface {
    Open(path string) (*parser.IDMLDocument, error)
    Close() error
}

// AssetLoader 负责将 URI 解析为可用的二进制数据。
type AssetLoader interface {
    Resolve(uri string, embedded map[string][]byte) assets.ResolveResult
}

// PDFRenderer 负责将解析后的元素绘制为 PDF。
type PDFRenderer interface {
    AddPage()
    DrawImage(data []byte, x, y, w, h float64) error
    DrawTextFrame(x, y, w, h float64, text, fontName string, fontSize float64)
    DrawPlaceholder(x, y, w, h float64, label string)
    Output(path string) error
}
```

### 3.3 关键结构体示例

```go
// Document 是解析后的文档模型（聚合自 designmap + spreads + stories）。
type Document struct {
    PageWidth  float64
    PageHeight float64
    Spreads    []Spread
    Stories    map[string]Story
    Fonts      []Font
}

// Page 表示单个页面（在 Spread 内）。
type Page struct {
    Name    string
    Bounds  GeometricBounds
    Items   []PageItem
}

// ImageAsset 表示解析后的图片素材（已加载为内存数据）。
type ImageAsset struct {
    Name        string
    Data        []byte
    IsEmbedded  bool
    IsPlaceholder bool
    SourceURI   string
}

// TextFrame 表示文本框及其关联的文本内容。
type TextFrame struct {
    Self      string
    Bounds    GeometricBounds
    Transform TransformMatrix
    StoryID   string
    Text      string
    FontName  string
    FontSize  float64
}
```

---

## 4. 关键技术实现步骤

### 4.1 使用 `archive/zip` 解压 IDML

```go
zr, err := zip.OpenReader(idmlPath)
if err != nil {
    return err
}
defer zr.Close()

for _, f := range zr.File {
    targetPath := filepath.Join(extractDir, f.Name)
    // ZipSlip 安全校验：确保目标路径在 extractDir 之下
    if !strings.HasPrefix(filepath.Clean(targetPath), filepath.Clean(extractDir)+string(os.PathSeparator)) {
        return fmt.Errorf("illegal zip entry: %s", f.Name)
    }
    // ... 解压逻辑
}
```

安全要点：必须做 `ZipSlip` 校验，防止恶意 IDML 通过 `../` 路径覆盖系统文件。

### 4.2 使用 `encoding/xml` 解析 XML

IDML 的 XML 命名空间较复杂，但解析时通常可以忽略命名空间，只关注本地标签名。定义结构体时使用 `xml.Name` 和标签属性映射：

```go
type PageItem struct {
    Self            string `xml:"Self,attr"`
    ItemTransform   string `xml:"ItemTransform,attr"`
    GeometricBounds string `xml:"GeometricBounds,attr"`
    Image           *Image `xml:"Image"`
    ParentStory     string `xml:"ParentStory,attr"`
}

type Image struct {
    Self string `xml:"Self,attr"`
    Link *Link  `xml:"Link"`
}

type Link struct {
    LinkResourceURI string `xml:"LinkResourceURI,attr"`
    StoredState     string `xml:"StoredState,attr"`
}
```

解析调用：

```go
func parseXMLFile(path string, v interface{}) error {
    data, err := os.ReadFile(path)
    if err != nil {
        return err
    }
    return xml.Unmarshal(data, v)
}
```

### 4.3 解析 `ItemTransform` 变换矩阵

IDML 的 `ItemTransform` 格式为六个空格分隔的浮点数：`m11 m12 m21 m22 tx ty`，对应 2D 仿射变换矩阵：

```
[ m11  m12  0 ]
[ m21  m22  0 ]
[ tx   ty   1 ]
```

该矩阵描述了从元素局部坐标系到父坐标系的映射，可同时编码**平移、旋转、缩放、倾斜**。解析代码见 `internal/parser/transform.go`：

```go
func ParseItemTransform(s string) (TransformMatrix, error) {
    parts := strings.Fields(s)
    if len(parts) != 6 {
        return m, fmt.Errorf("expected 6 fields, got %d", len(parts))
    }
    // ... 解析为 float64
}
```

### 4.4 从 `Resources/Graphics/` 读取嵌入图片

在解压循环中，检测路径前缀 `Resources/Graphics/`，直接将文件内容读入内存：

```go
if strings.HasPrefix(f.Name, "Resources/Graphics/") && !f.FileInfo().IsDir() {
    data, err := os.ReadFile(targetPath)
    if err == nil {
        doc.EmbeddedGraphics[filepath.Base(f.Name)] = data
    }
}
```

### 4.5 从 `Stories/*.xml` 提取文本内容和样式

Story 的 XML 结构采用**样式范围（Style Range）**模型：外层是 `ParagraphStyleRange`，内层是 `CharacterStyleRange`。文本内容分散在多个 `Content` 节点中：

```xml
<Story Self="Story_u1fb">
  <ParagraphStyleRange AppliedParagraphStyle="ParagraphStyle/正文">
    <CharacterStyleRange AppliedCharacterStyle="CharacterStyle/默认" PointSize="12" FillColor="Color/Black">
      <Content>这是一段示例文字</Content>
    </CharacterStyleRange>
    <CharacterStyleRange ...>
      <Content>，继续后续内容。</Content>
      <Br />   <!-- 换行 -->
    </CharacterStyleRange>
  </ParagraphStyleRange>
</Story>
```

提取逻辑：遍历所有 `CharacterStyleRange`，拼接 `Content` 字段。`Br` 标签表示换行。

### 4.6 PDF 生成库的选择与用法

本方案采用 **`github.com/signintech/gopdf`**（纯 Go 实现，无 CGO），原因如下：

- 纯 Go 实现，无 CGO 依赖，跨平台编译友好。
- API 简洁，支持图片嵌入、文本绘制、页面管理、TrueType 字体直接加载（`.ttf`）。
- 支持文本旋转、多行文本框（`MultiCell`）等实用功能。

**基本用法示例**：

```go
package main

import "github.com/signintech/gopdf"

func main() {
    pdf := gopdf.GoPdf{}
    pdf.Start(gopdf.Config{PageSize: *gopdf.PageSizeA4})
    pdf.AddPage()

    // 加载 TTF 字体并绘制文本
    _ = pdf.AddTTFFont("Arial", "/path/to/arial.ttf")
    _ = pdf.SetFont("Arial", "", 14)
    pdf.SetXY(100, 100)
    pdf.Cell(nil, "Hello IDML")

    // 绘制图片（x, y, 需指定尺寸矩形）
    _ = pdf.Image("logo.png", 100, 200, &gopdf.Rect{W: 200, H: 150})

    // 输出到文件
    _ = pdf.WritePdf("output.pdf")
}
```

**坐标系重要说明**：`gopdf` 的页面坐标系原点在**左上角**，Y 轴向下，与 IDML 完全一致。因此本方案无需做 Y 轴翻转，可直接将 IDML 的 `GeometricBounds` 坐标用于 PDF 绘制。

---

## 5. 处理难点与应对方案

### 5.1 字体映射（IDML 字体名 → 实际字体文件）

**难点**：IDML 中记录的字体名（如 `Adobe Song Std`、`Myriad Pro Bold`）与操作系统中的字体文件名往往不一致；中文字体尤其复杂。

**应对方案**：

1. **建立映射表**：维护一个 `map[string]string`，将 IDML 字体名映射到已加载的 TTF 字体名称。
2. **内置字体兜底**：对于无法映射的字体，回退到 `ArialUnicode` 或 `微软雅黑`（中文）/`Arial`（西文）。
3. **TrueType 字体直接加载**：使用 `gopdf` 的 `AddTTFFont` 方法直接加载 `.ttf` 字体：
   ```go
   pdf.AddTTFFont("CustomFont", "/path/to/custom-font.ttf")
   pdf.SetFont("CustomFont", "", 12)
   ```
4. **中文字体**：必须注册支持中文的 `.ttf` 字体（如 `MicrosoftYaHei.ttf`、`Arial Unicode.ttf`）。**注意**：macOS 上的 `.ttc` 字体集合文件不被 `gopdf` 支持，需要单独准备 `.ttf` 格式的中文字体。

### 5.2 坐标系转换（IDML → PDF）

IDML 和 PDF 的坐标系差异：

| 属性 | IDML | gopdf（API） |
|------|------|-------------|
| 原点 | 左上角 | 左上角 |
| Y 轴方向 | 向下 | 向下 |

**简化策略**：由于 `gopdf` 的 API 坐标系与 IDML 完全一致（左上角原点，Y 轴向下），因此无需翻转 Y 坐标，可直接将 IDML 的全局坐标减去页面偏移后传入 `gopdf`。

### 5.3 图片的裁剪与缩放（基于变换矩阵计算）

IDML 中图片框（PageItem）和图片本身（Image）各有一个 `ItemTransform` 和 `GeometricBounds`。要得到图片在页面上的最终显示区域，需要：

1. 将 `PageItem` 的变换矩阵与 `Image` 的变换矩阵相乘（`parentTransform * childTransform`）。
2. 将 `GeometricBounds` 的四个角点通过变换矩阵映射到父坐标系。
3. 计算映射后的外接矩形，得到最终的 `x, y, width, height`。

**简化处理**：在示例代码中，我们直接使用 `PageItem.GeometricBounds` 作为图片的绘制区域，忽略了 `Image` 内部的额外变换和裁剪路径。这对于大多数常规排版场景已足够，但对于包含复杂旋转/倾斜的图片，需要扩展 `TransformMatrix.Apply()` 对四个角点分别变换。

### 5.4 文本分栏、绕排等高级布局

**难点**：IDML 支持文本绕排（Text Wrap）、分栏（Column）、基线网格对齐、OpenType 特性等复杂排版。

**应对方案（分阶段实现）**：

| 阶段 | 策略 |
|------|------|
| 第一阶段（最小可用） | 仅提取纯文本，按文本框边界绘制，忽略绕排和分栏 |
| 第二阶段 | 实现文本框内的多栏：根据 `TextFrame::TextFramePreference::TextColumnCount` 计算栏宽，用 `MultiCellWithOption` 分栏渲染 |
| 第三阶段 | 实现绕排检测：读取 `PageItem::TextWrapMode`，在渲染文本前计算被绕排区域，使用 `gopdf` 的 `SetXY` 跳过被占据区域 |
| 第四阶段 | 精确排版：引入 HarfBuzz 或类似库处理复杂字形排版，或使用 Headless Chrome 将 HTML 排版结果转为 PDF（架构更复杂） |

**扩展思路**：如果项目对排版还原度要求极高，可考虑将 IDML 先转换为 HTML + CSS（利用 IDML 中丰富的样式信息），再通过浏览器引擎渲染为 PDF。这种方案能天然继承浏览器的分栏、绕排能力，但工程复杂度显著增加。

---

## 6. 完整可运行的示例代码

本项目已提供可直接编译运行的示例。项目结构如下：

```
idml-to-pdf/
├── go.mod
├── main.go                         <-- 简化示例入口
├── internal/
│   ├── parser/
│   │   ├── idml.go                 <-- ZIP 解压与 XML 解析
│   │   ├── xml_types.go            <-- XML 结构体定义
│   │   └── transform.go            <-- 变换矩阵解析
│   ├── assets/
│   │   └── loader.go               <-- 素材路径解析与回退
│   └── renderer/
│       └── pdf.go                  <-- 基于 gopdf 的 PDF 渲染
└── test/
```

### 6.1 编译与运行

```bash
cd /Users/Yau/work/1.Resources/3.qoder/idml-to-pdf
GOPROXY=https://goproxy.cn,direct go mod tidy
GOPROXY=https://goproxy.cn,direct go run main.go -idml ./test/1.idml -out ./test/output.pdf -assets ./test/assets
```

参数说明：

- `-idml`：输入的 IDML 文件路径（必填）。
- `-out`：输出的 PDF 文件路径（默认 `output.pdf`）。
- `-assets`：素材根目录，多个目录用逗号分隔。
- `-keep`：保留解压后的临时文件（用于调试）。

### 6.2 代码核心流程

`main.go` 的执行流程清晰对应了四个阶段：

1. **解析 IDML**：调用 `parser.OpenIDML` 解压 ZIP，解析 `designmap.xml`、`Spreads/*.xml`、`Stories/*.xml`，同时收集嵌入图片到内存。
2. **准备加载器**：创建 `assets.Loader`，配置素材根目录和占位图生成器。
3. **渲染 PDF**：遍历每个 `Spread`，展平所有 `PageItem`，对图片调用 `DrawImage`，对文本框调用 `DrawTextFrame`。
4. **保存文件**：调用 `rend.Output` 写入磁盘。

### 6.3 关键简化与已知限制

本示例为了可运行性做了以下简化，生产环境可按需扩展：

- **变换矩阵**：已实现将 `ItemTransform` 应用到边界框的四个角点，计算 axis-aligned bounding box。对于旋转元素，位置基本正确，但文本框的宽高是 bounding box 而非原始矩形尺寸，可能导致文本换行行为与 InDesign 不完全一致。
- **文本样式**：提取了纯文本、字体名、字号和对齐方式，但未应用 IDML 中的字符颜色、字重、行距、段落间距等详细样式。
- **字体**：已加载 `MicrosoftYaHei.ttf` 和 `Arial Unicode.ttf` 作为中文字体回退，macOS 上的 `.ttc` 集合字体不被 `gopdf` 支持。
- **高级元素**：未处理主页（MasterSpread）、图层（Layer）、表格（Table）、路径裁剪（ClippingPath）、文本绕排等。

---

## 7. 测试与验证建议

### 7.1 如何准备测试 IDML

由于 IDML 是 ZIP 包，可以手动构造一个最小化的测试文件：

1. 在 InDesign 中创建一个单页文档，放入一张链接图片和一个文本框。
2. 执行 `文件 → 导出 → IDML` 保存为 `sample.idml`。
3. 将 `sample.idml` 改名为 `sample.zip`，解压检查内部 XML 结构是否符合预期。
4. 将链接图片复制到 `./test/assets/` 目录，确保程序能通过回退机制找到。

### 7.2 功能测试清单

| 测试项 | 预期结果 | 验证方法 |
|--------|---------|---------|
| 单张链接图片 | PDF 中图片显示在正确位置 | 目视比对 |
| 单段文本 | PDF 中文字内容正确 | 复制 PDF 文本比对 |
| 嵌入图片 | 不依赖外部素材即可显示 | 删除外部素材后仍能生成 |
| 素材缺失 | PDF 中显示红色占位框 | 目视检查 |
| 多级回退 | 移动素材位置后仍能加载 | 修改 `-assets` 参数测试 |
| 多 Spread | 每个 Spread 对应一页 PDF | 检查页数 |

### 7.3 如何比对生成的 PDF 与 InDesign 导出的 PDF

**定性比对**：

- 使用 Adobe Acrobat 的「比较文件」功能（`工具 → 比较文件`），将 InDesign 原生导出的 PDF 与程序生成的 PDF 进行视觉差异比对。
- 或使用开源工具 `diff-pdf`（基于 Poppler）生成差异高亮图：
  ```bash
  diff-pdf --output-diff=diff.png indesign.pdf program.pdf
  ```

**定量比对**：

- 提取两版 PDF 中图片的坐标和尺寸，计算欧氏距离误差。
- 提取文本内容，用 `pdftotext` 对比字符级差异。
- 计算页面覆盖率（图片+文字区域占比），作为整体还原度指标。

**回归测试**：

- 准备一组覆盖不同场景的标准 IDML（单图、图文混排、多页、嵌入图、缺失图）。
- 在 CI 中运行转换程序，对比输出 PDF 的页数、文本 MD5、图片数量等元数据。
- 设置阈值（如坐标误差 < 2pt，文本匹配度 > 95%），自动判定是否通过。

---

## 附录：快速参考

### 依赖安装

```bash
go get github.com/signintech/gopdf
```

### 创建最小可测试 IDML 的命令行方法

如果不方便使用 InDesign，可以用以下 Bash 脚本手工构造一个极简 IDML（仅用于测试 ZIP 解析和路径提取逻辑）：

```bash
mkdir -p /tmp/minimal-idml/Spreads /tmp/minimal-idml/Stories /tmp/minimal-idml/Resources/Graphics

# 构造 designmap.xml
cat > /tmp/minimal-idml/designmap.xml <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<Document>
  <DocumentPreferences PageWidth="595" PageHeight="842"/>
  <Spreads><SpreadRef src="Spreads/Spread_1.xml" Self="Spread_1"/></Spreads>
  <Stories><StoryRef src="Stories/Story_1.xml" Self="Story_1"/></Stories>
</Document>
EOF

# 构造 Spread_1.xml（包含一个图片框和一个文本框）
cat > /tmp/minimal-idml/Spreads/Spread_1.xml <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<Spread Self="Spread_1">
  <Page Self="Page_1" Name="1" GeometricBounds="0 0 842 595"/>
  <Rectangle Self="Item_1" GeometricBounds="100 100 300 400" ItemTransform="1 0 0 1 0 0">
    <Image Self="Image_1">
      <Link Self="Link_1" LinkResourceURI="file:test.jpg" StoredState="Normal"/>
    </Image>
  </Rectangle>
  <TextFrame Self="Item_2" GeometricBounds="350 100 450 400" ItemTransform="1 0 0 1 0 0" ParentStory="Story_1"/>
</Spread>
EOF

# 构造 Story_1.xml
cat > /tmp/minimal-idml/Stories/Story_1.xml <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<Story Self="Story_1">
  <ParagraphStyleRange>
    <CharacterStyleRange><Content>Hello from IDML</Content></CharacterStyleRange>
  </ParagraphStyleRange>
</Story>
EOF

# 添加一个虚拟嵌入图片（实际为 1x1 PNG）
printf '\x89PNG\r\n\x1a\n' > /tmp/minimal-idml/Resources/Graphics/test.jpg

# 打包为 IDML
cd /tmp/minimal-idml && zip -r /tmp/test.idml .
```

执行后会在 `/tmp/test.idml` 生成一个最简 IDML 文件，配合 `-assets` 参数指向包含真实 `test.jpg` 的目录即可运行测试。

---

*本文档及配套代码由 QoderWork 生成，基于 Go 1.22+ 与 `github.com/signintech/gopdf` 构建。*
