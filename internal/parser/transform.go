// Package parser — 变换矩阵与坐标计算
//
// 这个文件处理 IDML 的 2D 仿射变换系统和边界计算。
//
// === 行向量矩阵格式（重要！）===
// IDML 的 ItemTransform 使用行向量（row-vector）格式：
//
//   [m11  m12]
//   [m21  m22]
//   [tx   ty ]
//
//   x' = m11*x + m21*y + tx
//   y' = m12*x + m22*y + ty
//
// 这与标准 PDF 的列向量格式不同。在标准列向量格式中：
//   [m11 m12 tx]    x' = m11*x + m12*y + tx
//   [m21 m22 ty]    y' = m21*x + m22*y + ty
//   [ 0   0   1]
//
// 对比发现：IDML 的行向量格式相当于标准矩阵的**转置**。
// m12 和 m21 的位置互换。这个区别对旋转方向计算至关重要。
//
// === 旋转公式 ===
// 对于行向量旋转矩阵 [cos θ, sin θ; -sin θ, cos θ]：
//   m11 = cos θ, m21 = sin θ → atan2(m21, m11) = θ
// 注意使用 m21 而不是 m12！
//
// === 坐标系统 ===
// IDML 使用 Y-down 坐标系（左上角为原点，Y 向下增加）。
// PDF 使用 Y-up 坐标系（左下角为原点，Y 向上增加）。
// gopdf 内部做 Y 轴翻转来适配。
package parser

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// TransformMatrix 表示 IDML 中 ItemTransform 的 2D 仿射变换矩阵。
//
// 格式: "m11 m12 m21 m22 tx ty"
//
// 矩阵元素含义：
//   M11, M12: 第1行 — 控制 X 方向的缩放/旋转（第一列在列向量格式中）
//   M21, M22: 第2行 — 控制 Y 方向的缩放/旋转（第二列在列向量格式中）
//   Tx, Ty:   平移量
//
// 常见模式：
//   纯平移: M11=1, M22=1, 其他=0
//   均匀缩放: M11=s, M22=s, 其他=0, Tx, Ty 有值
//   旋转 90°: M11=0, M12=1, M21=-1, M22=0 （或反过来）
//   镜像翻转: M11=1, M22=-1 （Y 轴翻转）
type TransformMatrix struct {
	M11, M12 float64
	M21, M22 float64
	Tx, Ty   float64
}

// ParseItemTransform 解析 ItemTransform 字符串。
// 输入格式: "m11 m12 m21 m22 tx ty"（6 个空格分隔的浮点数）
func ParseItemTransform(s string) (TransformMatrix, error) {
	var m TransformMatrix
	parts := strings.Fields(s)
	if len(parts) != 6 {
		return m, fmt.Errorf("invalid ItemTransform: expected 6 fields, got %d in %q", len(s), s)
	}
	vals := make([]float64, 6)
	for i, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return m, fmt.Errorf("invalid ItemTransform field %d: %w", i, err)
		}
		vals[i] = v
	}
	m.M11, m.M12 = vals[0], vals[1]
	m.M21, m.M22 = vals[2], vals[3]
	m.Tx, m.Ty = vals[4], vals[5]
	return m, nil
}

// GeometricBounds 解析 IDML 的 GeometricBounds 字符串。
// 格式: "y1 x1 y2 x2"（IDML 先写 Y 再写 X）
//
// 注意：IDML 的 GeometricBounds 是局部坐标（未应用 ItemTransform），
// 顺序是 (Y1, X1, Y2, X2)，与常见的 (X1, Y1, X2, Y2) 不同。
type GeometricBounds struct {
	Y1, X1 float64
	Y2, X2 float64
}

// ParseGeometricBounds 解析边界字符串。
func ParseGeometricBounds(s string) (GeometricBounds, error) {
	var b GeometricBounds
	parts := strings.Fields(s)
	if len(parts) != 4 {
		return b, fmt.Errorf("invalid GeometricBounds: expected 4 fields, got %d in %q", len(parts), s)
	}
	vals := make([]float64, 4)
	for i, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return b, fmt.Errorf("invalid GeometricBounds field %d: %w", i, err)
		}
		vals[i] = v
	}
	b.Y1, b.X1 = vals[0], vals[1]
	b.Y2, b.X2 = vals[2], vals[3]
	return b, nil
}

func (b GeometricBounds) Width() float64  { return b.X2 - b.X1 }
func (b GeometricBounds) Height() float64 { return b.Y2 - b.Y1 }

// Translate 创建一个纯平移矩阵。
func Translate(tx, ty float64) TransformMatrix {
	return TransformMatrix{M11: 1, M12: 0, M21: 0, M22: 1, Tx: tx, Ty: ty}
}

// Mul 矩阵乘法：a * b。
// 按照行向量格式：
//   结果[i][j] = sum_k a[i][k] * b[k][j]
// 平移部分：Tx = a.Tx*b.M11 + a.Ty*b.M21 + b.Tx
//           Ty = a.Tx*b.M12 + a.Ty*b.M22 + b.Ty
//
// 用于组嵌套：子元素先应用自身变换，再应用父组变换。
// 调用顺序：combined = parent.Mul(child)
func (a TransformMatrix) Mul(b TransformMatrix) TransformMatrix {
	return TransformMatrix{
		M11: a.M11*b.M11 + a.M12*b.M21,
		M12: a.M11*b.M12 + a.M12*b.M22,
		M21: a.M21*b.M11 + a.M22*b.M21,
		M22: a.M21*b.M12 + a.M22*b.M22,
		Tx:  a.Tx*b.M11 + a.Ty*b.M21 + b.Tx,
		Ty:  a.Tx*b.M12 + a.Ty*b.M22 + b.Ty,
	}
}

// Apply 对点 (x, y) 应用变换矩阵。
// 公式：
//   x' = M11*x + M21*y + Tx
//   y' = M12*x + M22*y + Ty
//
// 注意：这是行向量格式，M21 乘的是 y，M12 乘的是 x。
// 与标准列向量格式的 y' = M21*x + M22*y + Ty 不同。
func (m TransformMatrix) Apply(x, y float64) (float64, float64) {
	return m.M11*x + m.M21*y + m.Tx,
		m.M12*x + m.M22*y + m.Ty
}

// ExtractScale 提取缩放因子。
// sx = sqrt(m11² + m12²)，sy = sqrt(m21² + m22²)
// 在纯缩放+旋转矩阵中，sx 和 sy 分别代表 X 和 Y 方向的缩放。
// 注意：sx 和 sy 在旋转 90° 时可能会互换。
func (m TransformMatrix) ExtractScale() (sx, sy float64) {
	sx = math.Sqrt(m.M11*m.M11 + m.M12*m.M12)
	sy = math.Sqrt(m.M21*m.M21 + m.M22*m.M22)
	return
}

// ExtractRotation 提取旋转角度（弧度）。
//
// *** 关键实现 ***
// IDML 使用行向量格式。对于旋转矩阵：
//   [cos θ  sin θ]
//   [-sin θ cos θ]
//
// 行向量 Apply 公式：
//   x' = m11*x + m21*y + tx = cosθ*x + (-sinθ)*y + tx
//   y' = m12*x + m22*y + ty = sinθ*x + cosθ*y + ty
//
// 所以 m11 = cosθ, m21 = -sinθ
// 但等等... 让我们检查一个 90° 顺时针旋转：
//   IDML 格式: [0 1; -1 0] → 行向量
//   x' = 0*x + (-1)*y = -y
//   y' = 1*x + 0*y = x
//   这确实是 90° 顺时针（在 Y-down 坐标系中）
//
// 所以 m11 = cosθ = 0 → θ = ±90°
//    m21 = -sinθ = -1 → θ = 90°
//    atan2(m21, m11) = atan2(-1, 0) = -90°... 但应该是 90°？
//
// 实际上，在 Y-down 坐标系中顺时针旋转 90° 的公式是：
//   m11 = cos(90°) = 0
//   m21 = -sin(90°) = -1
//   atan2(-1, 0) = -π/2 = -90°
//
// 所以 ExtractRotation 返回的是**标准数学角度**（逆时针为正）。
// 而 gopdf 内部做 Y 翻转后，正角度在视觉上变成顺时针。
// 这就是为什么 main.go 中不需要加负号取反。
func (m TransformMatrix) ExtractRotation() float64 {
	// IDML 使用行向量格式: [m11 m21; m12 m22]
	// Apply: x' = m11*x + m21*y, y' = m12*x + m22*y
	// 旋转矩阵: [cos θ  sin θ; -sin θ  cos θ]
	// m11 = cos θ, m21 = -sin θ → atan2(-m21, m11) = θ
	//  但实际上... 让我们推导一下。
	//  标准旋转矩阵 R(θ) = [cosθ  -sinθ; sinθ  cosθ]
	//  Apply x' = m11*x + m21*y = cosθ*x + sinθ*y  ← 不对！
	//
	//  重新看：IDML 的 Apply 函数是：
	//    x' = M11*x + M21*y + Tx
	//    y' = M12*x + M22*y + Ty
	//
	//  对于旋转 90° (IDML: "0 1 -1 0"): M11=0, M12=1, M21=-1, M22=0
	//    x' = 0*x + (-1)*y = -y
	//    y' = 1*x + 0*y = x
	//  这在 Y-down 坐标系中 = 顺时针旋转 90°
	//
	//  atan2(M21, M11) = atan2(-1, 0) = -90°
	//  所以 ExtractRotation 返回 -90°（标准数学角度，逆时针为正）
	//  但我们需要正 90°（顺时针，对应 IDML 的视觉效果）
	//
	//  解决方案：由于 gopdf 内部做了 Y 翻转，正角度在视觉上变成顺时针，
	//  所以 IDML 的顺时针角度 = -ExtractRotation() 才是 gopdf 需要的。
	//  但经过试验发现 **gopdf 的 Y 翻转正好抵消了这个差异**，
	//  所以直接使用 ExtractRotation() 的返回值就可以了。
	//  详见 main.go:117-118 的注释。
	return math.Atan2(m.M21, m.M11)
}

// ParseAnchor 解析 PathPointType 的 Anchor 字符串 "x y"。
func ParseAnchor(s string) (x, y float64, err error) {
	parts := strings.Fields(s)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid anchor: %q", s)
	}
	x, err = strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, 0, err
	}
	y, err = strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return 0, 0, err
	}
	return x, y, nil
}

// ComputeBoundsFromPathGeometry 从 PathGeometry 计算局部边界框。
//
// PathGeometry 由一系列路径点定义，这些点都在局部坐标系中
// （未应用 ItemTransform）。此函数遍历所有 GeometryPathType 的
// PathPointArray，找到所有 Anchor 点的最小/最大坐标。
//
// 返回: 局部坐标系下的 x1, y1, x2, y2（IDML 坐标）
func ComputeBoundsFromPathGeometry(pg *PathGeometry) (x1, y1, x2, y2 float64, ok bool) {
	if pg == nil || len(pg.GeometryPaths) == 0 {
		return 0, 0, 0, 0, false
	}
	first := true
	for _, gp := range pg.GeometryPaths {
		for _, pp := range gp.PathPointArray {
			x, y, err := ParseAnchor(pp.Anchor)
			if err != nil {
				continue
			}
			if first {
				x1, y1, x2, y2 = x, y, x, y
				first = false
			} else {
				if x < x1 { x1 = x }
				if x > x2 { x2 = x }
				if y < y1 { y1 = y }
				if y > y2 { y2 = y }
			}
		}
	}
	return x1, y1, x2, y2, !first
}

// ComputeItemGlobalBounds 计算 PageItem 在 Spread 坐标系下的边界框。
//
// 步骤：
// 1. 获取局部边界（GeometricBounds → PathGeometry → EPS 边界）
// 2. 对四个角点应用 ItemTransform
// 3. 取变换后的最小/最大 X/Y 作为 AABB（轴对齐外接矩形）
//
// 返回值是 Spread 坐标系下的全局 AABB。
// 要得到页面坐标，需要减去 Page 的 ItemTransform 偏移。
//
// 局部边界获取优先级：
//   a. GeometricBounds（最准确，所有元素都有）
//   b. PathGeometry（没有 GeometricBounds 时的回退）
//   c. EPSTextAttributeBounds / PathBoundingBox（EPS 文本专用）
func ComputeItemGlobalBounds(it PageItem) (gx1, gy1, gx2, gy2 float64, err error) {
	var lx1, ly1, lx2, ly2 float64
	var hasLocal bool

	// 1. 尝试 GeometricBounds
	if it.GeometricBounds != "" {
		b, err := ParseGeometricBounds(it.GeometricBounds)
		if err == nil {
			lx1, ly1, lx2, ly2 = b.X1, b.Y1, b.X2, b.Y2
			hasLocal = true
		}
	}

	// 2. 尝试 PathGeometry
	if !hasLocal && it.Properties.PathGeometry != nil {
		var ok bool
		lx1, ly1, lx2, ly2, ok = ComputeBoundsFromPathGeometry(it.Properties.PathGeometry)
		hasLocal = ok
	}

	// 2b. 尝试 EPS 文本对象的边界信息
	if !hasLocal && it.Properties.EPSTextData != "" {
		if x1, y1, x2, y2, ok := computeBoundsFromRectStrings(
			it.Properties.EPSTextAttributeBounds.Left,
			it.Properties.EPSTextAttributeBounds.Top,
			it.Properties.EPSTextAttributeBounds.Right,
			it.Properties.EPSTextAttributeBounds.Bottom,
		); ok {
			lx1, ly1, lx2, ly2 = x1, y1, x2, y2
			hasLocal = true
		} else if x1, y1, x2, y2, ok := computeBoundsFromRectStrings(
			it.Properties.PathBoundingBox.Left,
			it.Properties.PathBoundingBox.Top,
			it.Properties.PathBoundingBox.Right,
			it.Properties.PathBoundingBox.Bottom,
		); ok {
			lx1, ly1, lx2, ly2 = x1, y1, x2, y2
			hasLocal = true
		}
	}

	if !hasLocal {
		return 0, 0, 0, 0, fmt.Errorf("no bounds available for item %s", it.Self)
	}

	// 3. 应用 ItemTransform
	m, err := ParseItemTransform(it.ItemTransform)
	if err != nil {
		m = Translate(0, 0)
	}

	// 对四个角点应用变换，计算外接矩形
	// 注意：旋转后的矩形需要取 AABB，不能直接用一个点加宽高
	corners := [][2]float64{{lx1, ly1}, {lx1, ly2}, {lx2, ly1}, {lx2, ly2}}
	first := true
	for _, c := range corners {
		gx, gy := m.Apply(c[0], c[1])
		if first {
			gx1, gy1, gx2, gy2 = gx, gy, gx, gy
			first = false
		} else {
			if gx < gx1 { gx1 = gx }
			if gx > gx2 { gx2 = gx }
			if gy < gy1 { gy1 = gy }
			if gy > gy2 { gy2 = gy }
		}
	}
	return gx1, gy1, gx2, gy2, nil
}

// GetItemLocalBounds 返回 PageItem 的局部边界（未应用 ItemTransform）。
//
// 与 ComputeItemGlobalBounds 不同，此函数返回变换前的原始坐标，
// 用于需要局部尺寸的场景（如 EPS 文本的字号计算、图片裁剪蒙版）。
func GetItemLocalBounds(it PageItem) (lx1, ly1, lx2, ly2 float64, ok bool) {
	if it.GeometricBounds != "" {
		b, err := ParseGeometricBounds(it.GeometricBounds)
		if err == nil {
			return b.X1, b.Y1, b.X2, b.Y2, true
		}
	}
	if it.Properties.PathGeometry != nil {
		return ComputeBoundsFromPathGeometry(it.Properties.PathGeometry)
	}
	if it.Properties.EPSTextData != "" {
		if x1, y1, x2, y2, ok := computeBoundsFromRectStrings(
			it.Properties.EPSTextAttributeBounds.Left,
			it.Properties.EPSTextAttributeBounds.Top,
			it.Properties.EPSTextAttributeBounds.Right,
			it.Properties.EPSTextAttributeBounds.Bottom,
		); ok {
			return x1, y1, x2, y2, true
		}
		if x1, y1, x2, y2, ok := computeBoundsFromRectStrings(
			it.Properties.PathBoundingBox.Left,
			it.Properties.PathBoundingBox.Top,
			it.Properties.PathBoundingBox.Right,
			it.Properties.PathBoundingBox.Bottom,
		); ok {
			return x1, y1, x2, y2, true
		}
	}
	return 0, 0, 0, 0, false
}

// ComputeItemBoundsSimple 返回简化后的 x, y, w, h（假设无旋转或旋转可忽略）。
// 用于不需要精确处理旋转角度的场景（如 EPS 文本、占位图）。
func ComputeItemBoundsSimple(it PageItem) (x, y, w, h float64, err error) {
	gx1, gy1, gx2, gy2, err := ComputeItemGlobalBounds(it)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return gx1, gy1, gx2 - gx1, gy2 - gy1, nil
}

// ComputeImageGlobalBounds 计算带有 Image/PDF 子元素的 PageItem 的全局边界。
//
// 返回：
//   - gx1, gy1, gx2, gy2: 父级全局边界（AABB），与 ComputeItemGlobalBounds 相同
//   - imgGx1, imgGy1: 图片的全局左上角位置（用于旋转时定位）
//   - angle: 合并后的旋转角度（父级变换 × 子级变换）
//
// 说明：
//   父级边界始终用于 PNG 转换和位置计算（像素对齐）。
//   图片位置和角度用于 DrawImage 时的正确旋转定位，
//   避免使用 AABB 尺寸绘制旋转图片导致的拉伸。
func ComputeImageGlobalBounds(it PageItem) (gx1, gy1, gx2, gy2, imgGx1, imgGy1, angle float64, err error) {
	// 边界始终使用父级变换计算的标准全局边界（避免 PNG 转换后
	// 再套用 child 变换导致位置/尺寸错误）。
	gx1, gy1, gx2, gy2, err = ComputeItemGlobalBounds(it)
	if err != nil {
		return 0, 0, 0, 0, 0, 0, 0, err
	}

	// 解析父级变换
	pm, err := ParseItemTransform(it.ItemTransform)
	if err != nil {
		pm = Translate(0, 0)
	}

	// 原图局部边界 → 全局位置（用于旋转时正确绘制，避免拉伸）
	lx1, ly1, _, _, hasLocal := GetItemLocalBounds(it)
	if hasLocal {
		// 原图全局左上角 = 局部左上角经过父级变换
		imgGx1 = pm.M11*lx1 + pm.M21*ly1 + pm.Tx
		imgGy1 = pm.M12*lx1 + pm.M22*ly1 + pm.Ty
	} else {
		imgGx1, imgGy1 = gx1, gy1
	}

	var combined TransformMatrix
	if it.PDF != nil && it.PDF.ItemTransform != "" {
		cm, _ := ParseItemTransform(it.PDF.ItemTransform)
		combined = pm.Mul(cm)
	} else if it.Image != nil && it.Image.ItemTransform != "" {
		cm, _ := ParseItemTransform(it.Image.ItemTransform)
		combined = pm.Mul(cm)
	} else {
		return gx1, gy1, gx2, gy2, imgGx1, imgGy1, pm.ExtractRotation(), nil
	}

	return gx1, gy1, gx2, gy2, imgGx1, imgGy1, combined.ExtractRotation(), nil
}

// TransformPathGeometry 将 PageItem 的 PathGeometry 点应用 ItemTransform，
// 返回全局坐标系下的点列表（格式为 [x1, y1, x2, y2, ...]）。
//
// 用于绘制多边形等不规则形状。
func TransformPathGeometry(it PageItem) ([]float64, error) {
	pg := it.Properties.PathGeometry
	if pg == nil || len(pg.GeometryPaths) == 0 {
		return nil, fmt.Errorf("no PathGeometry for item %s", it.Self)
	}

	m, err := ParseItemTransform(it.ItemTransform)
	if err != nil {
		m = Translate(0, 0)
	}

	var pts []float64
	for _, gp := range pg.GeometryPaths {
		for _, pp := range gp.PathPointArray {
			x, y, err := ParseAnchor(pp.Anchor)
			if err != nil {
				continue
			}
			gx, gy := m.Apply(x, y)
			pts = append(pts, gx, gy)
		}
	}
	if len(pts) < 4 {
		return nil, fmt.Errorf("insufficient points for item %s", it.Self)
	}
	return pts, nil
}

// computeBoundsFromRectStrings 将四个独立的字符串边界值解析为 GeometricBounds。
// 注意：IDML 的 EPSTextAttributeBounds 存储顺序为 (Left, Top, Right, Bottom)，
// 而 GeometricBounds 需要 "y1 x1 y2 x2"，所以需要调整顺序。
func computeBoundsFromRectStrings(left, top, right, bottom string) (x1, y1, x2, y2 float64, ok bool) {
	if left == "" || top == "" || right == "" || bottom == "" {
		return 0, 0, 0, 0, false
	}
	// 重排为: top left bottom right → y1 x1 y2 x2
	b, err := ParseGeometricBounds(top + " " + left + " " + bottom + " " + right)
	if err != nil {
		return 0, 0, 0, 0, false
	}
	return b.X1, b.Y1, b.X2, b.Y2, true
}