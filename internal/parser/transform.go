package parser

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// TransformMatrix 表示 IDML 中 ItemTransform 的 2D 仿射变换矩阵。
// 格式: "m11 m12 m21 m22 tx ty"
type TransformMatrix struct {
	M11, M12 float64
	M21, M22 float64
	Tx, Ty   float64
}

// ParseItemTransform 解析 ItemTransform 字符串。
func ParseItemTransform(s string) (TransformMatrix, error) {
	var m TransformMatrix
	parts := strings.Fields(s)
	if len(parts) != 6 {
		return m, fmt.Errorf("invalid ItemTransform: expected 6 fields, got %d in %q", len(parts), s)
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
// 格式: "y1 x1 y2 x2"
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

func Translate(tx, ty float64) TransformMatrix {
	return TransformMatrix{M11: 1, M12: 0, M21: 0, M22: 1, Tx: tx, Ty: ty}
}

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

func (m TransformMatrix) Apply(x, y float64) (float64, float64) {
	return m.M11*x + m.M21*y + m.Tx,
		m.M12*x + m.M22*y + m.Ty
}

func (m TransformMatrix) ExtractScale() (sx, sy float64) {
	sx = math.Sqrt(m.M11*m.M11 + m.M12*m.M12)
	sy = math.Sqrt(m.M21*m.M21 + m.M22*m.M22)
	return
}

func (m TransformMatrix) ExtractRotation() float64 {
	// IDML 使用行向量格式: [m11 m21; m12 m22]
	// Apply: x' = m11*x + m21*y, y' = m12*x + m22*y
	// 旋转矩阵: [cos θ  sin θ; -sin θ  cos θ]
	// m11 = cos θ, m21 = sin θ → atan2(m21, m11) = θ
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
// 返回局部坐标系下的 x1, y1, x2, y2。
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
// 优先使用 GeometricBounds，其次从 PathGeometry 计算。
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
		// 默认恒等变换
		m = Translate(0, 0)
	}

	// 对四个角点应用变换，计算外接矩形
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
func ComputeItemBoundsSimple(it PageItem) (x, y, w, h float64, err error) {
	gx1, gy1, gx2, gy2, err := ComputeItemGlobalBounds(it)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return gx1, gy1, gx2 - gx1, gy2 - gy1, nil
}

// ComputeImageGlobalBounds 计算带有 Image/PDF 子元素的 PageItem 的全局边界。
// 返回：
//   - gx1, gy1, gx2, gy2: 父级全局边界（AABB）
//   - imgGx1, imgGy1: 图片/底图的全局左上角位置（未经过旋转拉伸）
//   - angle: 合并后的旋转角度
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
		// 原图全局左上角 = 局部左上角 经过父级变换
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

func computeBoundsFromRectStrings(left, top, right, bottom string) (x1, y1, x2, y2 float64, ok bool) {
	if left == "" || top == "" || right == "" || bottom == "" {
		return 0, 0, 0, 0, false
	}
	b, err := ParseGeometricBounds(top + " " + left + " " + bottom + " " + right)
	if err != nil {
		return 0, 0, 0, 0, false
	}
	return b.X1, b.Y1, b.X2, b.Y2, true
}


