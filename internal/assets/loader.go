// Package assets 负责素材路径的解析、查找和加载。
// 核心能力：
// 1. 解析 IDML 中 LinkResourceURI 的各种格式（file://、绝对路径、相对路径）
// 2. 多级回退查找：原始路径 → 用户指定根目录 → 按文件名递归搜索
// 3. 区分嵌入素材和链接素材
// 4. 缺失素材时返回占位图并记录日志
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
	AssetRoots []string

	// FallbackSearch 为 true 时，会在 AssetRoots 下按文件名递归搜索。
	FallbackSearch bool

	// PlaceholderGenerator 在素材缺失时生成占位图像数据。
	// 如果为 nil，则返回错误。
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
	// ResolvedPath 是最终解析到的本地文件系统绝对路径（空表示未找到）。
	ResolvedPath string
	// Data 是读取到的文件内容。
	Data []byte
	// IsEmbedded 表示是否为嵌入素材。
	IsEmbedded bool
	// IsPlaceholder 表示是否使用了占位图。
	IsPlaceholder bool
	// Err 如果非 nil，表示加载失败且无法回退。
	Err error
}

// Resolve 解析 URI 并加载素材数据。
// 处理流程：
//  1. 如果是空 URI，返回错误。
//  2. 去掉 file:// 前缀。
//  3. 尝试直接访问解析后的路径。
//  4. 如果失败，依次尝试 AssetRoots。
//  5. 如果仍失败且 FallbackSearch=true，按文件名在 AssetRoots 下递归搜索。
//  6. 如果全部失败，尝试生成占位图。
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
func cleanURI(uri string) string {
	s := uri
	// 处理 file:///C:/path 和 file:/path 等变体
	if strings.HasPrefix(s, "file://") {
		s = s[len("file://"):]
		// Windows 路径 file:///C:/path -> /C:/path -> C:/path
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

func tryReadFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return nil, fmt.Errorf("not a file")
	}
	return os.ReadFile(path)
}

// searchByName 在 root 目录下递归搜索名为 name 的文件。
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
