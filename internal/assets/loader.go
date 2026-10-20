// Package assets 负责素材路径的解析、查找和加载。
//
// 核心功能：
// 1. 解析 IDML 中 LinkResourceURI 的各种格式（file://、绝对路径、相对路径）
// 2. 多级回退查找：原始路径 → 用户指定根目录 → 按文件名递归搜索
// 3. 区分嵌入素材和链接素材（嵌入素材优先从 IDML 包的 EmbeddedGraphics 中获取）
// 4. 缺失素材时返回占位图并记录日志
//
// 搜索策略（优先级顺序）：
//   a. 嵌入素材缓存（embeddedData 中按文件名查找）
//   b. URI 清洗后的原始路径
//   c. 用户指定的 AssetRoots + 原始相对路径
//   d. 用户指定的 AssetRoots + 仅文件名
//   e. AssetRoots 下按文件名递归搜索
//   f. 生成占位图
//
// 典型的 URI 格式：
//   file:///Users/yau/Desktop/image.jpg  — macOS 绝对路径
//   file:///C:/Users/me/image.jpg       — Windows 绝对路径
//   链接/图片/logo.png                    — 相对路径
//   image.jpg                            — 仅文件名
package assets

import (
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Loader 是素材加载器，包含多级回退策略。
type Loader struct {
	// AssetRoots 是用户指定的素材搜索根目录列表。
	// 当原始路径不可访问时，会依次在这些目录下查找。
	// 通常在命令行通过 -assets 参数指定，多个目录用逗号分隔。
	AssetRoots []string

	// FallbackSearch 为 true 时，会在 AssetRoots 下按文件名递归搜索。
	// 默认启用，适用于素材文件分散在多级子目录中的情况。
	FallbackSearch bool

	// PlaceholderGenerator 在素材缺失时生成占位图像数据。
	// 如果为 nil，无素材时返回错误而非占位图。
	// 类型签名：func(name string, w, h float64) ([]byte, error)
	PlaceholderGenerator func(name string, w, h float64) ([]byte, error)

	// Logger 用于记录素材加载和回退事件。
	Logger *log.Logger
}

// NewLoader 创建默认素材加载器。
func NewLoader(roots []string) *Loader {
	return &Loader{
		AssetRoots:     roots,
		FallbackSearch: true,
		Logger:         log.New(io.Discard, "[assets] ", log.LstdFlags),
	}
}

// ResolveResult 表示路径解析和加载结果。
type ResolveResult struct {
	// OriginalURI 是 IDML 中记录的原始 URI。
	OriginalURI string
	// ResolvedPath 是最终解析到的本地文件系统绝对路径（空表示未找到或使用占位图）。
	ResolvedPath string
	// Data 是读取到的文件内容（字节切片）。
	Data []byte
	// IsEmbedded 表示是否为嵌入素材（来自 IDML 包的 Resources/Graphics/）。
	IsEmbedded bool
	// IsPlaceholder 表示是否使用了占位图。
	IsPlaceholder bool
	// Err 如果非 nil，表示加载失败且无法回退。
	Err error
}

// Resolve 解析 URI 并加载素材数据。
//
// 处理流程：
// 1. 空值保护 — 空 URI 直接返回错误
// 2. 嵌入素材优先 — 在 embeddedData 中按文件名查找
// 3. URI 清洗 — 去掉 file:// 前缀、URL 解码
// 4. 直接访问 — 尝试访问清洗后的路径
// 5. 多根目录拼接 — 依次尝试 AssetRoots + 相对路径
// 6. 仅文件名拼接 — AssetRoots + 仅文件名
// 7. 递归搜索 — 在 AssetRoots 下按文件名搜索
// 8. 占位图 — 如果上述全部失败，生成占位图
//
// 参数：
//   uri: IDML 中的 LinkResourceURI
//   embeddedData: IDMLDocument.EmbeddedGraphics（key = 文件名）
func (l *Loader) Resolve(uri string, embeddedData map[string][]byte) ResolveResult {
	res := ResolveResult{OriginalURI: uri}

	// 空值保护
	if strings.TrimSpace(uri) == "" {
		res.Err = fmt.Errorf("empty asset URI")
		return res
	}

	// 1. 判断是否为嵌入素材引用
	// 嵌入素材通常只包含文件名，且该文件名存在于 embeddedData 中
	base := filepath.Base(uri)
	if data, ok := embeddedData[base]; ok {
		res.Data = data
		res.IsEmbedded = true
		res.ResolvedPath = "embedded://" + base
		l.log("使用嵌入素材: %s", base)
		return res
	}

	// 2. 清洗 URI：去掉 file:// 或 file:/// 前缀
	cleaned := cleanURI(uri)

	// 3. 尝试直接访问（绝对路径或相对于当前工作目录）
	if data, err := tryReadFile(cleaned); err == nil {
		res.Data = data
		res.ResolvedPath = cleaned
		l.log("直接加载素材: %s", cleaned)
		return res
	}

	// 4. 尝试基于 AssetRoots 的相对路径拼接
	for _, root := range l.AssetRoots {
		candidate := filepath.Join(root, cleaned)
		if data, err := tryReadFile(candidate); err == nil {
			res.Data = data
			res.ResolvedPath = candidate
			l.log("从根目录加载素材: %s", candidate)
			return res
		}
		// 有时 IDML 存储的是绝对路径，而素材实际在根目录下
		// 此时尝试只用文件名拼接
		candidate = filepath.Join(root, base)
		if data, err := tryReadFile(candidate); err == nil {
			res.Data = data
			res.ResolvedPath = candidate
			l.log("从根目录(仅文件名)加载素材: %s", candidate)
			return res
		}
	}

	// 5. 递归搜索（FallbackSearch）
	if l.FallbackSearch {
		for _, root := range l.AssetRoots {
			if found := searchByName(root, base); found != "" {
				if data, err := tryReadFile(found); err == nil {
					res.Data = data
					res.ResolvedPath = found
					l.log("递归搜索找到素材: %s -> %s", base, found)
					return res
				}
			}
		}
	}

	// 6. 生成占位图
	l.log("素材缺失，生成占位图: %s", base)
	if l.PlaceholderGenerator != nil {
		data, err := l.PlaceholderGenerator(base, 200, 150)
		if err == nil {
			res.Data = data
			res.IsPlaceholder = true
			res.ResolvedPath = "placeholder://" + base
			return res
		}
	}

	res.Err = fmt.Errorf("asset not found: %s (searched in %v)", uri, l.AssetRoots)
	return res
}

// cleanURI 去掉 file:// 前缀、URL 解码并做路径清洗。
//
// 处理的 URI 变体：
//   file:///Users/yau/image.jpg → /Users/yau/image.jpg
//   file:///C:/Users/me/img.jpg  → C:/Users/me/img.jpg
//   file:/relative/path        → relative/path
//   ../assets/img.jpg          → ../assets/img.jpg
//   %E6%96%B0.jpg             → 新.jpg (URL 解码)
func cleanURI(uri string) string {
	s := uri
	// 处理 file:///C:/path 和 file:/path 等变体
	if strings.HasPrefix(s, "file://") {
		s = s[len("file://"):]
		// Windows 路径 file:///C:/path → /C:/path → C:/path
		if len(s) >= 2 && s[0] == '/' && s[2] == ':' {
			s = s[1:]
		}
	} else if strings.HasPrefix(s, "file:") {
		s = s[len("file:"):]
	}
	// URL 解码（处理 %E6%96%B0 等编码字符）
	if decoded, err := url.PathUnescape(s); err == nil {
		s = decoded
	}
	// 统一分隔符
	return filepath.Clean(s)
}

// tryReadFile 安全地尝试读取文件，目录或不存在时返回错误。
func tryReadFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return nil, fmt.Errorf("not a file")
	}
	return os.ReadFile(path)
}

// searchByName 在 root 目录下递归搜索名为 name 的文件。
// 使用 filepath.Walk 遍历目录树，忽略大小写匹配文件名。
// 返回第一个匹配的完整路径。
// 使用 io.EOF 作为提前终止信号来停止 Walk。
func searchByName(root, name string) string {
	var result string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.EqualFold(info.Name(), name) {
			result = path
			return io.EOF // 用 EOF 作为提前终止信号
		}
		return nil
	})
	return result
}

func (l *Loader) log(format string, v ...interface{}) {
	if l.Logger != nil {
		l.Logger.Printf(format, v...)
	}
}