package step

import "math"

// Vec3 is a 3D vector.
type Vec3 struct{ X, Y, Z float64 }

func (a Vec3) Add(b Vec3) Vec3      { return Vec3{a.X + b.X, a.Y + b.Y, a.Z + b.Z} }
func (a Vec3) Sub(b Vec3) Vec3      { return Vec3{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }
func (a Vec3) Scale(s float64) Vec3 { return Vec3{a.X * s, a.Y * s, a.Z * s} }
func (a Vec3) Dot(b Vec3) float64   { return a.X*b.X + a.Y*b.Y + a.Z*b.Z }
func (a Vec3) Cross(b Vec3) Vec3 {
	return Vec3{a.Y*b.Z - a.Z*b.Y, a.Z*b.X - a.X*b.Z, a.X*b.Y - a.Y*b.X}
}
func (a Vec3) Len() float64         { return math.Sqrt(a.Dot(a)) }
func (a Vec3) Dist(b Vec3) float64  { return a.Sub(b).Len() }
func (a Vec3) Dist2(b Vec3) float64 { d := a.Sub(b); return d.Dot(d) }
func (a Vec3) Lerp(b Vec3, t float64) Vec3 {
	return Vec3{a.X + (b.X-a.X)*t, a.Y + (b.Y-a.Y)*t, a.Z + (b.Z-a.Z)*t}
}

// Norm returns a unit vector in the direction of a, or the zero vector.
func (a Vec3) Norm() Vec3 {
	l := a.Len()
	if l < 1e-300 {
		return Vec3{}
	}
	return a.Scale(1 / l)
}

// AnyPerp returns some unit vector perpendicular to a.
func (a Vec3) AnyPerp() Vec3 {
	if math.Abs(a.X) < 0.9 {
		return a.Cross(Vec3{1, 0, 0}).Norm()
	}
	return a.Cross(Vec3{0, 1, 0}).Norm()
}

// Affine is a 3D affine transform p' = R*p + T.
type Affine struct {
	R [3][3]float64 // row-major
	T Vec3
}

// Identity returns the identity transform.
func Identity() Affine {
	return Affine{R: [3][3]float64{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}}
}

// Frame returns the transform mapping local coordinates in the frame with
// the given origin and axes to global coordinates.
func Frame(origin, x, y, z Vec3) Affine {
	return Affine{
		R: [3][3]float64{
			{x.X, y.X, z.X},
			{x.Y, y.Y, z.Y},
			{x.Z, y.Z, z.Z},
		},
		T: origin,
	}
}

// ScaleAffine returns a uniform scale.
func ScaleAffine(s float64) Affine {
	return Affine{R: [3][3]float64{{s, 0, 0}, {0, s, 0}, {0, 0, s}}}
}

// Apply transforms a point.
func (m Affine) Apply(p Vec3) Vec3 {
	return Vec3{
		m.R[0][0]*p.X + m.R[0][1]*p.Y + m.R[0][2]*p.Z + m.T.X,
		m.R[1][0]*p.X + m.R[1][1]*p.Y + m.R[1][2]*p.Z + m.T.Y,
		m.R[2][0]*p.X + m.R[2][1]*p.Y + m.R[2][2]*p.Z + m.T.Z,
	}
}

// ApplyDir transforms a direction (no translation).
func (m Affine) ApplyDir(p Vec3) Vec3 {
	return Vec3{
		m.R[0][0]*p.X + m.R[0][1]*p.Y + m.R[0][2]*p.Z,
		m.R[1][0]*p.X + m.R[1][1]*p.Y + m.R[1][2]*p.Z,
		m.R[2][0]*p.X + m.R[2][1]*p.Y + m.R[2][2]*p.Z,
	}
}

// ApplyNormal transforms a surface normal (inverse transpose of R), and
// normalizes the result.
func (m Affine) ApplyNormal(n Vec3) Vec3 {
	inv := m.Inverse()
	return Vec3{
		inv.R[0][0]*n.X + inv.R[1][0]*n.Y + inv.R[2][0]*n.Z,
		inv.R[0][1]*n.X + inv.R[1][1]*n.Y + inv.R[2][1]*n.Z,
		inv.R[0][2]*n.X + inv.R[1][2]*n.Y + inv.R[2][2]*n.Z,
	}.Norm()
}

// Mul returns m∘n (apply n first, then m).
func (m Affine) Mul(n Affine) Affine {
	var r Affine
	for i := range 3 {
		for j := range 3 {
			r.R[i][j] = m.R[i][0]*n.R[0][j] + m.R[i][1]*n.R[1][j] + m.R[i][2]*n.R[2][j]
		}
	}
	r.T = m.Apply(n.T)
	return r
}

// Det returns the determinant of the linear part.
func (m Affine) Det() float64 {
	a := m.R
	return a[0][0]*(a[1][1]*a[2][2]-a[1][2]*a[2][1]) -
		a[0][1]*(a[1][0]*a[2][2]-a[1][2]*a[2][0]) +
		a[0][2]*(a[1][0]*a[2][1]-a[1][1]*a[2][0])
}

// Inverse returns the inverse transform. Singular transforms return the
// identity.
func (m Affine) Inverse() Affine {
	a := m.R
	det := m.Det()
	if math.Abs(det) < 1e-300 {
		return Identity()
	}
	id := 1 / det
	var r Affine
	r.R[0][0] = (a[1][1]*a[2][2] - a[1][2]*a[2][1]) * id
	r.R[0][1] = (a[0][2]*a[2][1] - a[0][1]*a[2][2]) * id
	r.R[0][2] = (a[0][1]*a[1][2] - a[0][2]*a[1][1]) * id
	r.R[1][0] = (a[1][2]*a[2][0] - a[1][0]*a[2][2]) * id
	r.R[1][1] = (a[0][0]*a[2][2] - a[0][2]*a[2][0]) * id
	r.R[1][2] = (a[0][2]*a[1][0] - a[0][0]*a[1][2]) * id
	r.R[2][0] = (a[1][0]*a[2][1] - a[1][1]*a[2][0]) * id
	r.R[2][1] = (a[0][1]*a[2][0] - a[0][0]*a[2][1]) * id
	r.R[2][2] = (a[0][0]*a[1][1] - a[0][1]*a[1][0]) * id
	r.T = r.ApplyDir(m.T).Scale(-1)
	return r
}

// Box is an axis aligned bounding box.
type Box struct {
	Min, Max Vec3
}

// EmptyBox returns a box that contains nothing.
func EmptyBox() Box {
	inf := math.Inf(1)
	return Box{Min: Vec3{inf, inf, inf}, Max: Vec3{-inf, -inf, -inf}}
}

// Empty reports whether the box contains no points.
func (b Box) Empty() bool { return b.Min.X > b.Max.X }

// Extend grows the box to include p.
func (b *Box) Extend(p Vec3) {
	b.Min.X = math.Min(b.Min.X, p.X)
	b.Min.Y = math.Min(b.Min.Y, p.Y)
	b.Min.Z = math.Min(b.Min.Z, p.Z)
	b.Max.X = math.Max(b.Max.X, p.X)
	b.Max.Y = math.Max(b.Max.Y, p.Y)
	b.Max.Z = math.Max(b.Max.Z, p.Z)
}

// Union grows the box to include o.
func (b *Box) Union(o Box) {
	if o.Empty() {
		return
	}
	b.Extend(o.Min)
	b.Extend(o.Max)
}

// Diag returns the length of the box diagonal.
func (b Box) Diag() float64 {
	if b.Empty() {
		return 0
	}
	return b.Max.Dist(b.Min)
}

// Center returns the center of the box.
func (b Box) Center() Vec3 { return b.Min.Add(b.Max).Scale(0.5) }

// Transform returns the bounding box of the transformed box corners.
func (b Box) Transform(m Affine) Box {
	if b.Empty() {
		return b
	}
	out := EmptyBox()
	for i := range 8 {
		p := Vec3{b.Min.X, b.Min.Y, b.Min.Z}
		if i&1 != 0 {
			p.X = b.Max.X
		}
		if i&2 != 0 {
			p.Y = b.Max.Y
		}
		if i&4 != 0 {
			p.Z = b.Max.Z
		}
		out.Extend(m.Apply(p))
	}
	return out
}

// wrapAngle maps a to (-pi, pi].
func wrapPeriod(a, period float64) float64 {
	a = math.Mod(a, period)
	if a > period/2 {
		a -= period
	} else if a <= -period/2 {
		a += period
	}
	return a
}
