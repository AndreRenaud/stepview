package step

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
)

// UV is a surface parameter pair.
type UV struct{ U, V float64 }

// UVBox is a parameter-space rectangle.
type UVBox struct{ U0, U1, V0, V1 float64 }

// Curve is a parametric 3D curve.
type Curve interface {
	// EvalD returns the point and first derivative at t.
	EvalD(t float64) (Vec3, Vec3)
	// Range returns the natural parameter range (possibly infinite).
	Range() (float64, float64)
	// Period returns the parameter period for closed periodic curves, or 0.
	Period() float64
	// Project returns the parameter of the point on the curve closest to p.
	Project(p Vec3) float64
	// InitialSegments returns a suggested segment count for [ta, tb].
	InitialSegments(ta, tb, tol, maxAngle float64) int
}

// Surface is a parametric surface.
type Surface interface {
	Eval(u, v float64) Vec3
	// Normal returns the unit normal in the direction of dS/du x dS/dv, or
	// the zero vector at singular points.
	Normal(u, v float64) Vec3
	// Project returns the parameters of the point closest to p. For periodic
	// directions the result is near hint when hasHint is set.
	Project(p Vec3, hint UV, hasHint bool) UV
	PeriodU() float64
	PeriodV() float64
	// Domain returns the natural parameter bounds; unbounded directions use
	// infinities.
	Domain() (u0, u1, v0, v1 float64, ok bool)
	// Scale returns approximate magnitudes of dS/du and dS/dv.
	Scale() (float64, float64)
	// Singular reports whether u (resp. v) is degenerate at (u, v), i.e.
	// changing it does not move the point (a pole or apex).
	Singular(u, v float64) (bool, bool)
	// GridLines returns interior parameter lines used to seed the
	// triangulation of curved faces within the box.
	GridLines(tol, maxAngle float64, b UVBox) ([]float64, []float64)
	IsPlane() bool
}

// angleStep returns the angular step giving chord error below tol on a
// circle of radius r, capped at maxAngle.
func angleStep(r, tol, maxAngle float64) float64 {
	if r <= tol {
		return maxAngle
	}
	a := 2 * math.Acos(1-tol/r)
	if !(a > 0) {
		return maxAngle
	}
	return math.Min(a, maxAngle)
}

// linesIn returns evenly spaced values strictly within (lo, hi) with
// spacing at most step.
func linesIn(lo, hi, step float64, maxN int) []float64 {
	if !(hi > lo) || !(step > 0) || math.IsInf(lo, 0) || math.IsInf(hi, 0) {
		return nil
	}
	n := int(math.Ceil((hi - lo) / step))
	n = min(n, maxN)
	if n < 2 {
		return nil
	}
	out := make([]float64, 0, n-1)
	for i := 1; i < n; i++ {
		out = append(out, lo+(hi-lo)*float64(i)/float64(n))
	}
	return out
}

// nearPeriod returns a + k*period closest to ref.
func nearPeriod(a, ref, period float64) float64 {
	if period <= 0 {
		return a
	}
	return a + math.Round((ref-a)/period)*period
}

// ---------------------------------------------------------------------------
// Curves

type lineCurve struct {
	o, d Vec3
}

func (c *lineCurve) EvalD(t float64) (Vec3, Vec3) { return c.o.Add(c.d.Scale(t)), c.d }
func (c *lineCurve) Range() (float64, float64)    { return math.Inf(-1), math.Inf(1) }
func (c *lineCurve) Period() float64              { return 0 }
func (c *lineCurve) Project(p Vec3) float64 {
	den := c.d.Dot(c.d)
	if den == 0 {
		return 0
	}
	return p.Sub(c.o).Dot(c.d) / den
}
func (c *lineCurve) InitialSegments(ta, tb, tol, maxAngle float64) int { return 1 }

type circleCurve struct {
	o, x, y Vec3
	r       float64
}

func (c *circleCurve) EvalD(t float64) (Vec3, Vec3) {
	s, co := math.Sincos(t)
	p := c.o.Add(c.x.Scale(c.r * co)).Add(c.y.Scale(c.r * s))
	d := c.x.Scale(-c.r * s).Add(c.y.Scale(c.r * co))
	return p, d
}
func (c *circleCurve) Range() (float64, float64) { return 0, 2 * math.Pi }
func (c *circleCurve) Period() float64           { return 2 * math.Pi }
func (c *circleCurve) Project(p Vec3) float64 {
	d := p.Sub(c.o)
	return math.Atan2(d.Dot(c.y), d.Dot(c.x))
}
func (c *circleCurve) InitialSegments(ta, tb, tol, maxAngle float64) int {
	return max(1, int(math.Ceil(math.Abs(tb-ta)/angleStep(c.r, tol, maxAngle))))
}

type ellipseCurve struct {
	o, x, y Vec3
	a, b    float64
}

func (c *ellipseCurve) EvalD(t float64) (Vec3, Vec3) {
	s, co := math.Sincos(t)
	p := c.o.Add(c.x.Scale(c.a * co)).Add(c.y.Scale(c.b * s))
	d := c.x.Scale(-c.a * s).Add(c.y.Scale(c.b * co))
	return p, d
}
func (c *ellipseCurve) Range() (float64, float64) { return 0, 2 * math.Pi }
func (c *ellipseCurve) Period() float64           { return 2 * math.Pi }
func (c *ellipseCurve) Project(p Vec3) float64 {
	d := p.Sub(c.o)
	t := math.Atan2(d.Dot(c.y)/c.b, d.Dot(c.x)/c.a)
	// Newton refinement for points slightly off the curve.
	for range 8 {
		q, dq := c.EvalD(t)
		s, co := math.Sincos(t)
		ddq := c.x.Scale(-c.a * co).Add(c.y.Scale(-c.b * s))
		r := q.Sub(p)
		f := r.Dot(dq)
		df := dq.Dot(dq) + r.Dot(ddq)
		if df == 0 {
			break
		}
		t -= f / df
	}
	return t
}
func (c *ellipseCurve) InitialSegments(ta, tb, tol, maxAngle float64) int {
	r := math.Max(c.a, c.b)
	return max(1, int(math.Ceil(math.Abs(tb-ta)/angleStep(r, tol, maxAngle))))
}

// polyCurve is a piecewise linear curve with parameter t in [0, n-1].
type polyCurve struct {
	pts []Vec3
}

func (c *polyCurve) EvalD(t float64) (Vec3, Vec3) {
	n := len(c.pts)
	if n == 1 {
		return c.pts[0], Vec3{}
	}
	i := int(math.Floor(t))
	i = max(0, min(n-2, i))
	f := t - float64(i)
	d := c.pts[i+1].Sub(c.pts[i])
	return c.pts[i].Add(d.Scale(f)), d
}
func (c *polyCurve) Range() (float64, float64) { return 0, float64(len(c.pts) - 1) }
func (c *polyCurve) Period() float64           { return 0 }
func (c *polyCurve) Project(p Vec3) float64 {
	best, bestT := math.Inf(1), 0.0
	for i := 0; i+1 < len(c.pts); i++ {
		a, b := c.pts[i], c.pts[i+1]
		ab := b.Sub(a)
		den := ab.Dot(ab)
		f := 0.0
		if den > 0 {
			f = math.Max(0, math.Min(1, p.Sub(a).Dot(ab)/den))
		}
		if d := a.Add(ab.Scale(f)).Dist2(p); d < best {
			best = d
			bestT = float64(i) + f
		}
	}
	return bestT
}
func (c *polyCurve) InitialSegments(ta, tb, tol, maxAngle float64) int {
	return max(1, int(math.Ceil(math.Abs(tb-ta))))
}

// ---------------------------------------------------------------------------
// Surfaces

type frame struct {
	o, x, y, z Vec3
}

func (f frame) local(p Vec3) Vec3 {
	d := p.Sub(f.o)
	return Vec3{d.Dot(f.x), d.Dot(f.y), d.Dot(f.z)}
}

func (f frame) global(x, y, z float64) Vec3 {
	return f.o.Add(f.x.Scale(x)).Add(f.y.Scale(y)).Add(f.z.Scale(z))
}

type planeSurface struct{ f frame }

func (s *planeSurface) Eval(u, v float64) Vec3   { return s.f.global(u, v, 0) }
func (s *planeSurface) Normal(u, v float64) Vec3 { return s.f.z }
func (s *planeSurface) Project(p Vec3, _ UV, _ bool) UV {
	l := s.f.local(p)
	return UV{l.X, l.Y}
}
func (s *planeSurface) PeriodU() float64 { return 0 }
func (s *planeSurface) PeriodV() float64 { return 0 }
func (s *planeSurface) Domain() (float64, float64, float64, float64, bool) {
	return math.Inf(-1), math.Inf(1), math.Inf(-1), math.Inf(1), false
}
func (s *planeSurface) Scale() (float64, float64)          { return 1, 1 }
func (s *planeSurface) Singular(u, v float64) (bool, bool) { return false, false }
func (s *planeSurface) GridLines(tol, maxAngle float64, b UVBox) ([]float64, []float64) {
	return nil, nil
}
func (s *planeSurface) IsPlane() bool { return true }

type cylinderSurface struct {
	f frame
	r float64
}

func (s *cylinderSurface) Eval(u, v float64) Vec3 {
	sn, c := math.Sincos(u)
	return s.f.global(s.r*c, s.r*sn, v)
}
func (s *cylinderSurface) Normal(u, v float64) Vec3 {
	sn, c := math.Sincos(u)
	return s.f.x.Scale(c).Add(s.f.y.Scale(sn))
}
func (s *cylinderSurface) Project(p Vec3, hint UV, hasHint bool) UV {
	l := s.f.local(p)
	u := math.Atan2(l.Y, l.X)
	if hasHint {
		u = nearPeriod(u, hint.U, 2*math.Pi)
	}
	return UV{u, l.Z}
}
func (s *cylinderSurface) PeriodU() float64 { return 2 * math.Pi }
func (s *cylinderSurface) PeriodV() float64 { return 0 }
func (s *cylinderSurface) Domain() (float64, float64, float64, float64, bool) {
	return 0, 2 * math.Pi, math.Inf(-1), math.Inf(1), false
}
func (s *cylinderSurface) Scale() (float64, float64)          { return s.r, 1 }
func (s *cylinderSurface) Singular(u, v float64) (bool, bool) { return false, false }
func (s *cylinderSurface) GridLines(tol, maxAngle float64, b UVBox) ([]float64, []float64) {
	// Rulings are straight, so the boundary samples plus centroid refinement
	// suffice; only the seam/pole helpers use the u lines.
	return linesIn(b.U0, b.U1, angleStep(s.r, tol, maxAngle), 512), nil
}
func (s *cylinderSurface) IsPlane() bool { return false }

type coneSurface struct {
	f   frame
	r   float64
	tan float64
}

func (s *coneSurface) radius(v float64) float64 { return s.r + v*s.tan }

func (s *coneSurface) Eval(u, v float64) Vec3 {
	sn, c := math.Sincos(u)
	rr := s.radius(v)
	return s.f.global(rr*c, rr*sn, v)
}
func (s *coneSurface) Normal(u, v float64) Vec3 {
	sn, c := math.Sincos(u)
	n := s.f.x.Scale(c).Add(s.f.y.Scale(sn)).Add(s.f.z.Scale(-s.tan)).Norm()
	if s.radius(v) < 0 {
		n = n.Scale(-1)
	}
	return n
}
func (s *coneSurface) Project(p Vec3, hint UV, hasHint bool) UV {
	l := s.f.local(p)
	u := math.Atan2(l.Y, l.X)
	if s.radius(l.Z) < 0 {
		u += math.Pi
	}
	if hasHint {
		u = nearPeriod(u, hint.U, 2*math.Pi)
	}
	return UV{u, l.Z}
}
func (s *coneSurface) PeriodU() float64 { return 2 * math.Pi }
func (s *coneSurface) PeriodV() float64 { return 0 }
func (s *coneSurface) apex() float64    { return -s.r / s.tan }
func (s *coneSurface) Domain() (float64, float64, float64, float64, bool) {
	if s.tan > 0 {
		return 0, 2 * math.Pi, s.apex(), math.Inf(1), false
	}
	return 0, 2 * math.Pi, math.Inf(-1), s.apex(), false
}
func (s *coneSurface) Scale() (float64, float64) {
	return math.Max(s.r, 1e-9), math.Sqrt(1 + s.tan*s.tan)
}
func (s *coneSurface) Singular(u, v float64) (bool, bool) {
	return math.Abs(s.radius(v)) < 1e-9*(1+math.Abs(s.r)), false
}
func (s *coneSurface) GridLines(tol, maxAngle float64, b UVBox) ([]float64, []float64) {
	rmax := math.Max(math.Abs(s.radius(b.V0)), math.Abs(s.radius(b.V1)))
	step := angleStep(rmax, tol, maxAngle)
	return linesIn(b.U0, b.U1, step, 512), nil
}
func (s *coneSurface) IsPlane() bool { return false }

type sphereSurface struct {
	f frame
	r float64
}

func (s *sphereSurface) Eval(u, v float64) Vec3 {
	su, cu := math.Sincos(u)
	sv, cv := math.Sincos(v)
	return s.f.global(s.r*cv*cu, s.r*cv*su, s.r*sv)
}
func (s *sphereSurface) Normal(u, v float64) Vec3 {
	su, cu := math.Sincos(u)
	sv, cv := math.Sincos(v)
	return s.f.x.Scale(cv * cu).Add(s.f.y.Scale(cv * su)).Add(s.f.z.Scale(sv))
}
func (s *sphereSurface) Project(p Vec3, hint UV, hasHint bool) UV {
	l := s.f.local(p)
	u := math.Atan2(l.Y, l.X)
	v := math.Atan2(l.Z, math.Hypot(l.X, l.Y))
	if hasHint {
		u = nearPeriod(u, hint.U, 2*math.Pi)
	}
	return UV{u, v}
}
func (s *sphereSurface) PeriodU() float64 { return 2 * math.Pi }
func (s *sphereSurface) PeriodV() float64 { return 0 }
func (s *sphereSurface) Domain() (float64, float64, float64, float64, bool) {
	return 0, 2 * math.Pi, -math.Pi / 2, math.Pi / 2, true
}
func (s *sphereSurface) Scale() (float64, float64) { return s.r, s.r }
func (s *sphereSurface) Singular(u, v float64) (bool, bool) {
	return math.Abs(math.Cos(v)) < 1e-9, false
}
func (s *sphereSurface) GridLines(tol, maxAngle float64, b UVBox) ([]float64, []float64) {
	step := angleStep(s.r, tol, maxAngle)
	return linesIn(b.U0, b.U1, step, 512), linesIn(b.V0, b.V1, step, 256)
}
func (s *sphereSurface) IsPlane() bool { return false }

type torusSurface struct {
	f    frame
	R, r float64
}

func (s *torusSurface) Eval(u, v float64) Vec3 {
	su, cu := math.Sincos(u)
	sv, cv := math.Sincos(v)
	rr := s.R + s.r*cv
	return s.f.global(rr*cu, rr*su, s.r*sv)
}
func (s *torusSurface) Normal(u, v float64) Vec3 {
	su, cu := math.Sincos(u)
	sv, cv := math.Sincos(v)
	n := s.f.x.Scale(cv * cu).Add(s.f.y.Scale(cv * su)).Add(s.f.z.Scale(sv))
	if s.R+s.r*cv < 0 {
		n = n.Scale(-1)
	}
	return n
}
func (s *torusSurface) Project(p Vec3, hint UV, hasHint bool) UV {
	l := s.f.local(p)
	u := math.Atan2(l.Y, l.X)
	rho := math.Hypot(l.X, l.Y) - s.R
	v := math.Atan2(l.Z, rho)
	if hasHint {
		u = nearPeriod(u, hint.U, 2*math.Pi)
		v = nearPeriod(v, hint.V, 2*math.Pi)
	}
	return UV{u, v}
}
func (s *torusSurface) PeriodU() float64 { return 2 * math.Pi }
func (s *torusSurface) PeriodV() float64 { return 2 * math.Pi }
func (s *torusSurface) Domain() (float64, float64, float64, float64, bool) {
	return 0, 2 * math.Pi, 0, 2 * math.Pi, true
}
func (s *torusSurface) Scale() (float64, float64) { return s.R + s.r, s.r }
func (s *torusSurface) Singular(u, v float64) (bool, bool) {
	return math.Abs(s.R+s.r*math.Cos(v)) < 1e-9*(s.R+s.r), false
}
func (s *torusSurface) GridLines(tol, maxAngle float64, b UVBox) ([]float64, []float64) {
	return linesIn(b.U0, b.U1, angleStep(s.R+s.r, tol, maxAngle), 512),
		linesIn(b.V0, b.V1, angleStep(s.r, tol, maxAngle), 256)
}
func (s *torusSurface) IsPlane() bool { return false }

// curveSamples holds adaptive samples of a curve used for projections.
type sampledCurve struct {
	c      Curve
	t0, t1 float64
	ts     []float64
	ps     []Vec3
}

func newSampledCurve(c Curve, t0, t1 float64, n int) *sampledCurve {
	sc := &sampledCurve{c: c, t0: t0, t1: t1}
	for i := 0; i <= n; i++ {
		t := t0 + (t1-t0)*float64(i)/float64(n)
		p, _ := c.EvalD(t)
		sc.ts = append(sc.ts, t)
		sc.ps = append(sc.ps, p)
	}
	return sc
}

// curveRange returns a usable bounded range for a curve used to sweep a
// surface. Unbounded curves (lines) get a large symmetric range.
func boundedRange(c Curve) (float64, float64) {
	t0, t1 := c.Range()
	if math.IsInf(t0, 0) || math.IsInf(t1, 0) {
		return -1e4, 1e4
	}
	return t0, t1
}

type revolutionSurface struct {
	c      Curve
	a, d   Vec3 // axis point and unit direction
	x, y   Vec3 // reference frame perpendicular to d
	t0, t1 float64
	samp   *sampledCurve
	sh, sr []float64 // per-sample height and radius
	sphi   []float64
	rmax   float64
	dmean  float64
}

func newRevolution(c Curve, a, d Vec3) *revolutionSurface {
	s := &revolutionSurface{c: c, a: a, d: d.Norm()}
	s.x = s.d.AnyPerp()
	s.y = s.d.Cross(s.x)
	s.t0, s.t1 = boundedRange(c)
	s.samp = newSampledCurve(c, s.t0, s.t1, 128)
	for i, p := range s.samp.ps {
		h, r, phi := s.cyl(p)
		s.sh = append(s.sh, h)
		s.sr = append(s.sr, r)
		s.sphi = append(s.sphi, phi)
		s.rmax = math.Max(s.rmax, r)
		_, dp := c.EvalD(s.samp.ts[i])
		s.dmean += dp.Len()
	}
	s.dmean /= float64(len(s.samp.ps))
	return s
}

func (s *revolutionSurface) cyl(p Vec3) (h, r, phi float64) {
	q := p.Sub(s.a)
	h = q.Dot(s.d)
	x, y := q.Dot(s.x), q.Dot(s.y)
	return h, math.Hypot(x, y), math.Atan2(y, x)
}

func rotateAbout(v, axis Vec3, ang float64) Vec3 {
	sn, c := math.Sincos(ang)
	return v.Scale(c).Add(axis.Cross(v).Scale(sn)).Add(axis.Scale(axis.Dot(v) * (1 - c)))
}

func (s *revolutionSurface) derivs(u, v float64) (Vec3, Vec3, Vec3) {
	p, dp := s.c.EvalD(v)
	q := rotateAbout(p.Sub(s.a), s.d, u)
	P := s.a.Add(q)
	Su := s.d.Cross(q)
	Sv := rotateAbout(dp, s.d, u)
	return P, Su, Sv
}

func (s *revolutionSurface) Eval(u, v float64) Vec3 {
	p, _, _ := s.derivs(u, v)
	return p
}
func (s *revolutionSurface) Normal(u, v float64) Vec3 {
	_, su, sv := s.derivs(u, v)
	return su.Cross(sv).Norm()
}
func (s *revolutionSurface) Project(p Vec3, hint UV, hasHint bool) UV {
	h, r, phi := s.cyl(p)
	// Find the curve parameter whose (h, r) is closest.
	best, bestD := 0, math.Inf(1)
	for i := range s.sh {
		dh, dr := s.sh[i]-h, s.sr[i]-r
		if d := dh*dh + dr*dr; d < bestD {
			bestD = d
			best = i
		}
	}
	t := s.samp.ts[best]
	// Newton on f(t) = (h(t)-h)^2 + (r(t)-r)^2 using numeric derivatives.
	eps := (s.t1 - s.t0) * 1e-7
	f := func(t float64) float64 {
		q, _ := s.c.EvalD(t)
		qh, qr, _ := s.cyl(q)
		return (qh-h)*(qh-h) + (qr-r)*(qr-r)
	}
	for range 30 {
		f0 := f(t)
		fp := f(math.Min(s.t1, t+eps))
		fm := f(math.Max(s.t0, t-eps))
		d1 := (fp - fm) / (2 * eps)
		d2 := (fp - 2*f0 + fm) / (eps * eps)
		if d2 <= 0 {
			break
		}
		nt := math.Max(s.t0, math.Min(s.t1, t-d1/d2))
		if math.Abs(nt-t) < eps*0.1 {
			t = nt
			break
		}
		t = nt
	}
	q, _ := s.c.EvalD(t)
	_, _, qphi := s.cyl(q)
	u := phi - qphi
	if hasHint {
		u = nearPeriod(u, hint.U, 2*math.Pi)
	}
	return UV{u, t}
}
func (s *revolutionSurface) PeriodU() float64 { return 2 * math.Pi }
func (s *revolutionSurface) PeriodV() float64 { return 0 }
func (s *revolutionSurface) Domain() (float64, float64, float64, float64, bool) {
	return 0, 2 * math.Pi, s.t0, s.t1, true
}
func (s *revolutionSurface) Scale() (float64, float64) {
	return math.Max(s.rmax, 1e-9), math.Max(s.dmean, 1e-9)
}
func (s *revolutionSurface) Singular(u, v float64) (bool, bool) {
	p, _ := s.c.EvalD(v)
	_, r, _ := s.cyl(p)
	return r < 1e-9*(1+s.rmax), false
}
func (s *revolutionSurface) GridLines(tol, maxAngle float64, b UVBox) ([]float64, []float64) {
	us := linesIn(b.U0, b.U1, angleStep(s.rmax, tol, maxAngle), 512)
	vs := curveParamLines(s.c, b.V0, b.V1, tol, maxAngle)
	return us, vs
}
func (s *revolutionSurface) IsPlane() bool { return false }

// curveParamLines returns adaptive sample parameters of c inside (lo, hi).
func curveParamLines(c Curve, lo, hi, tol, maxAngle float64) []float64 {
	if !(hi > lo) || math.IsInf(lo, 0) || math.IsInf(hi, 0) {
		return nil
	}
	ts := sampleCurveParams(c, lo, hi, tol, maxAngle)
	if len(ts) <= 2 {
		return nil
	}
	return ts[1 : len(ts)-1]
}

type extrusionSurface struct {
	c      Curve
	e      Vec3 // extrusion vector
	en     Vec3 // unit extrusion direction
	t0, t1 float64
	samp   *sampledCurve
	dmean  float64
}

func newExtrusion(c Curve, e Vec3) *extrusionSurface {
	s := &extrusionSurface{c: c, e: e, en: e.Norm()}
	s.t0, s.t1 = boundedRange(c)
	s.samp = newSampledCurve(c, s.t0, s.t1, 128)
	for _, t := range s.samp.ts {
		_, d := c.EvalD(t)
		s.dmean += d.Len()
	}
	s.dmean /= float64(len(s.samp.ts))
	return s
}

func (s *extrusionSurface) Eval(u, v float64) Vec3 {
	p, _ := s.c.EvalD(u)
	return p.Add(s.e.Scale(v))
}
func (s *extrusionSurface) Normal(u, v float64) Vec3 {
	_, d := s.c.EvalD(u)
	return d.Cross(s.e).Norm()
}
func (s *extrusionSurface) perp(p Vec3) Vec3 { return p.Sub(s.en.Scale(p.Dot(s.en))) }
func (s *extrusionSurface) Project(p Vec3, hint UV, hasHint bool) UV {
	pp := s.perp(p)
	best, bestD := 0, math.Inf(1)
	for i, q := range s.samp.ps {
		if d := s.perp(q).Dist2(pp); d < bestD {
			bestD = d
			best = i
		}
	}
	t := s.samp.ts[best]
	for range 30 {
		q, d := s.c.EvalD(t)
		r := s.perp(q).Sub(pp)
		dp := s.perp(d)
		den := dp.Dot(dp)
		if den < 1e-300 {
			break
		}
		nt := math.Max(s.t0, math.Min(s.t1, t-r.Dot(dp)/den))
		if math.Abs(nt-t) < 1e-12*(1+math.Abs(t)) {
			t = nt
			break
		}
		t = nt
	}
	q, _ := s.c.EvalD(t)
	v := p.Sub(q).Dot(s.e) / s.e.Dot(s.e)
	return UV{t, v}
}
func (s *extrusionSurface) PeriodU() float64 { return 0 }
func (s *extrusionSurface) PeriodV() float64 { return 0 }
func (s *extrusionSurface) Domain() (float64, float64, float64, float64, bool) {
	return s.t0, s.t1, math.Inf(-1), math.Inf(1), false
}
func (s *extrusionSurface) Scale() (float64, float64) {
	return math.Max(s.dmean, 1e-9), math.Max(s.e.Len(), 1e-9)
}
func (s *extrusionSurface) Singular(u, v float64) (bool, bool) { return false, false }
func (s *extrusionSurface) GridLines(tol, maxAngle float64, b UVBox) ([]float64, []float64) {
	return curveParamLines(s.c, b.U0, b.U1, tol, maxAngle), nil
}
func (s *extrusionSurface) IsPlane() bool { return false }

type offsetSurface struct {
	b Surface
	d float64
}

func (s *offsetSurface) Eval(u, v float64) Vec3 {
	return s.b.Eval(u, v).Add(s.b.Normal(u, v).Scale(s.d))
}
func (s *offsetSurface) Normal(u, v float64) Vec3 { return s.b.Normal(u, v) }
func (s *offsetSurface) Project(p Vec3, hint UV, hasHint bool) UV {
	uv := s.b.Project(p, hint, hasHint)
	for range 3 {
		q := p.Sub(s.b.Normal(uv.U, uv.V).Scale(s.d))
		uv = s.b.Project(q, uv, true)
	}
	return uv
}
func (s *offsetSurface) PeriodU() float64 { return s.b.PeriodU() }
func (s *offsetSurface) PeriodV() float64 { return s.b.PeriodV() }
func (s *offsetSurface) Domain() (float64, float64, float64, float64, bool) {
	return s.b.Domain()
}
func (s *offsetSurface) Scale() (float64, float64) { return s.b.Scale() }
func (s *offsetSurface) Singular(u, v float64) (bool, bool) {
	return s.b.Singular(u, v)
}
func (s *offsetSurface) GridLines(tol, maxAngle float64, b UVBox) ([]float64, []float64) {
	us, vs := s.b.GridLines(tol, maxAngle, b)
	if s.b.IsPlane() {
		return nil, nil
	}
	return us, vs
}
func (s *offsetSurface) IsPlane() bool { return s.b.IsPlane() }

// ---------------------------------------------------------------------------
// Entity conversion

// geomCache converts curve and surface entities, caching results. It is
// safe for concurrent use.
type geomCache struct {
	f        *File
	curves   sync.Map // id -> Curve or error
	surfaces sync.Map // id -> Surface or error
}

type geomErr struct{ err error }

func (g *geomCache) point(v Value) (Vec3, error) {
	e := g.f.Ref(v)
	if e == nil {
		return Vec3{}, errors.New("missing point")
	}
	args, ok := e.PartArgs("CARTESIAN_POINT")
	if !ok && e.Is("VERTEX_POINT") {
		// Resolve one level only: a self-referencing vertex must not recurse.
		if p := g.f.Ref(e.Arg(1)); p != nil {
			e = p
			args, ok = e.PartArgs("CARTESIAN_POINT")
		}
	}
	if !ok || len(args) < 2 {
		return Vec3{}, fmt.Errorf("expected CARTESIAN_POINT, got %s", e.Type)
	}
	return coords(args[1].AsList()), nil
}

func coords(l []Value) Vec3 {
	var p Vec3
	if len(l) > 0 {
		p.X = l[0].AsFloat()
	}
	if len(l) > 1 {
		p.Y = l[1].AsFloat()
	}
	if len(l) > 2 {
		p.Z = l[2].AsFloat()
	}
	return p
}

func (g *geomCache) direction(v Value) (Vec3, bool) {
	e := g.f.Ref(v)
	if e == nil {
		return Vec3{}, false
	}
	if e.Is("VECTOR") {
		d, ok := g.direction(e.Arg(1))
		return d.Scale(e.Arg(2).AsFloat()), ok
	}
	args, ok := e.PartArgs("DIRECTION")
	if !ok || len(args) < 2 {
		return Vec3{}, false
	}
	return coords(args[1].AsList()), true
}

// placement reads an AXIS2_PLACEMENT_3D (or 2D) as an orthonormal frame.
func (g *geomCache) placement(v Value) (frame, error) {
	e := g.f.Ref(v)
	if e == nil {
		return frame{}, errors.New("missing placement")
	}
	switch {
	case e.Is("AXIS2_PLACEMENT_3D"):
		o, err := g.point(e.Arg(1))
		if err != nil {
			return frame{}, err
		}
		z, ok := g.direction(e.Arg(2))
		if !ok || z.Len() == 0 {
			z = Vec3{0, 0, 1}
		}
		z = z.Norm()
		x, ok := g.direction(e.Arg(3))
		x = x.Sub(z.Scale(x.Dot(z)))
		if !ok || x.Len() < 1e-12 {
			x = z.AnyPerp()
			if math.Abs(z.Z) > 0.9 {
				x = Vec3{1, 0, 0}.Sub(z.Scale(z.X)).Norm()
			}
		}
		x = x.Norm()
		return frame{o: o, x: x, y: z.Cross(x), z: z}, nil
	case e.Is("AXIS2_PLACEMENT_2D"):
		o, err := g.point(e.Arg(1))
		if err != nil {
			return frame{}, err
		}
		x, ok := g.direction(e.Arg(2))
		if !ok || x.Len() == 0 {
			x = Vec3{1, 0, 0}
		}
		x = x.Norm()
		z := Vec3{0, 0, 1}
		return frame{o: o, x: x, y: z.Cross(x), z: z}, nil
	case e.Is("AXIS1_PLACEMENT"):
		o, err := g.point(e.Arg(1))
		if err != nil {
			return frame{}, err
		}
		z, ok := g.direction(e.Arg(2))
		if !ok || z.Len() == 0 {
			z = Vec3{0, 0, 1}
		}
		z = z.Norm()
		x := z.AnyPerp()
		return frame{o: o, x: x, y: z.Cross(x), z: z}, nil
	}
	return frame{}, fmt.Errorf("unsupported placement %s", e.Type)
}

// Curve returns the 3D curve for an entity id.
func (g *geomCache) curve(id int) (Curve, error) {
	if v, ok := g.curves.Load(id); ok {
		if ge, ok := v.(geomErr); ok {
			return nil, ge.err
		}
		return v.(Curve), nil
	}
	c, err := g.buildCurve(id, 0)
	if err != nil {
		g.curves.Store(id, geomErr{err})
		return nil, err
	}
	g.curves.Store(id, c)
	return c, nil
}

func (g *geomCache) buildCurve(id int, depth int) (Curve, error) {
	if depth > 16 {
		return nil, errors.New("curve nesting too deep")
	}
	e := g.f.Get(id)
	if e == nil {
		return nil, fmt.Errorf("missing curve #%d", id)
	}
	switch {
	case e.Type == "LINE":
		o, err := g.point(e.Arg(1))
		if err != nil {
			return nil, err
		}
		d, ok := g.direction(e.Arg(2))
		if !ok {
			return nil, errors.New("bad line direction")
		}
		return &lineCurve{o: o, d: d}, nil
	case e.Type == "CIRCLE":
		f, err := g.placement(e.Arg(1))
		if err != nil {
			return nil, err
		}
		return &circleCurve{o: f.o, x: f.x, y: f.y, r: e.Arg(2).AsFloat()}, nil
	case e.Type == "ELLIPSE":
		f, err := g.placement(e.Arg(1))
		if err != nil {
			return nil, err
		}
		return &ellipseCurve{o: f.o, x: f.x, y: f.y, a: e.Arg(2).AsFloat(), b: e.Arg(3).AsFloat()}, nil
	case e.Type == "TRIMMED_CURVE":
		return g.buildCurve(e.Arg(1).Ref, depth+1)
	case e.Type == "SURFACE_CURVE" || e.Type == "SEAM_CURVE" || e.Type == "INTERSECTION_CURVE" || e.Type == "BOUNDED_SURFACE_CURVE":
		return g.buildCurve(e.Arg(1).Ref, depth+1)
	case e.Type == "POLYLINE":
		var pts []Vec3
		for _, v := range e.Arg(1).AsList() {
			p, err := g.point(v)
			if err != nil {
				return nil, err
			}
			pts = append(pts, p)
		}
		if len(pts) < 2 {
			return nil, errors.New("degenerate polyline")
		}
		return &polyCurve{pts: pts}, nil
	case e.Is("B_SPLINE_CURVE_WITH_KNOTS") || e.Is("BEZIER_CURVE") || e.Is("UNIFORM_CURVE") || e.Is("QUASI_UNIFORM_CURVE"):
		return g.bsplineCurve(e)
	case e.Type == "COMPOSITE_CURVE":
		var pts []Vec3
		for _, sv := range e.Arg(1).AsList() {
			seg := g.f.Ref(sv)
			if seg == nil {
				continue
			}
			c, err := g.buildCurve(seg.Arg(2).Ref, depth+1)
			if err != nil {
				return nil, err
			}
			t0, t1 := c.Range()
			if math.IsInf(t0, 0) || math.IsInf(t1, 0) {
				return nil, errors.New("unbounded composite segment")
			}
			ts := sampleCurveParams(c, t0, t1, 0, math.Pi/16)
			if !seg.Arg(1).AsBool() {
				for i, j := 0, len(ts)-1; i < j; i, j = i+1, j-1 {
					ts[i], ts[j] = ts[j], ts[i]
				}
			}
			for _, t := range ts {
				p, _ := c.EvalD(t)
				if len(pts) == 0 || pts[len(pts)-1].Dist(p) > 1e-12 {
					pts = append(pts, p)
				}
			}
		}
		if len(pts) < 2 {
			return nil, errors.New("degenerate composite curve")
		}
		return &polyCurve{pts: pts}, nil
	}
	return nil, fmt.Errorf("unsupported curve %s", entityTypeName(e))
}

func entityTypeName(e *Entity) string {
	if e.Type != "" {
		return e.Type
	}
	var s strings.Builder
	s.WriteString("(")
	for i, p := range e.Parts {
		if i > 0 {
			s.WriteString(" ")
		}
		s.WriteString(p.Type)
	}
	return s.String() + ")"
}

func ints(l []Value) []int {
	out := make([]int, len(l))
	for i, v := range l {
		out[i] = int(v.AsFloat())
	}
	return out
}

func floats(l []Value) []float64 {
	out := make([]float64, len(l))
	for i, v := range l {
		out[i] = v.AsFloat()
	}
	return out
}

// uniformKnots builds knot vectors for the implicit-knot B-spline subtypes.
func implicitKnots(e *Entity, nCtrl, p int) []float64 {
	m := nCtrl + p + 1
	U := make([]float64, m)
	switch {
	case e.Is("BEZIER_CURVE") || e.Is("BEZIER_SURFACE"):
		// Piecewise Bezier: knots repeated p times; for a single segment
		// this is a clamped [0,0..,1,1..] vector.
		segs := (nCtrl - 1) / p
		k := 0
		for i := 0; i <= p; i++ {
			U[k] = 0
			k++
		}
		for s := 1; s < segs; s++ {
			for range p {
				U[k] = float64(s)
				k++
			}
		}
		for k < m {
			U[k] = float64(max(1, segs))
			k++
		}
	case e.Is("QUASI_UNIFORM_CURVE") || e.Is("QUASI_UNIFORM_SURFACE"):
		for i := range U {
			switch {
			case i <= p:
				U[i] = 0
			case i >= m-p-1:
				U[i] = float64(m - 2*p - 1)
			default:
				U[i] = float64(i - p)
			}
		}
	default: // uniform
		for i := range U {
			U[i] = float64(i - p)
		}
	}
	return U
}

func (g *geomCache) bsplineCurve(e *Entity) (Curve, error) {
	var deg int
	var ctrlV []Value
	if args, ok := e.PartArgs("B_SPLINE_CURVE"); ok && e.Type == "" {
		if len(args) < 2 {
			return nil, errors.New("bad B_SPLINE_CURVE")
		}
		deg = int(args[0].AsFloat())
		ctrlV = args[1].AsList()
	} else {
		deg = int(e.Arg(1).AsFloat())
		ctrlV = e.Arg(2).AsList()
	}
	ctrl := make([]Vec3, 0, len(ctrlV))
	for _, v := range ctrlV {
		p, err := g.point(v)
		if err != nil {
			return nil, err
		}
		ctrl = append(ctrl, p)
	}
	var U []float64
	if e.Type == "" {
		if args, ok := e.PartArgs("B_SPLINE_CURVE_WITH_KNOTS"); ok && len(args) >= 2 {
			U = expandKnots(floats(args[1].AsList()), ints(args[0].AsList()))
		}
	} else if e.Type == "B_SPLINE_CURVE_WITH_KNOTS" {
		U = expandKnots(floats(e.Arg(7).AsList()), ints(e.Arg(6).AsList()))
	}
	if U == nil {
		U = implicitKnots(e, len(ctrl), deg)
	}
	var w []float64
	if args, ok := e.PartArgs("RATIONAL_B_SPLINE_CURVE"); ok && len(args) >= 1 {
		w = floats(args[0].AsList())
		if len(w) != len(ctrl) {
			return nil, errors.New("weight count mismatch")
		}
	}
	return newBSplineCurve(deg, ctrl, w, U)
}

// surface returns the surface for an entity id. angleScale converts plane
// angle measures to radians.
func (g *geomCache) surface(id int, angleScale float64) (Surface, error) {
	if v, ok := g.surfaces.Load(id); ok {
		if ge, ok := v.(geomErr); ok {
			return nil, ge.err
		}
		return v.(Surface), nil
	}
	s, err := g.buildSurface(id, angleScale, 0)
	if err != nil {
		g.surfaces.Store(id, geomErr{err})
		return nil, err
	}
	g.surfaces.Store(id, s)
	return s, nil
}

func (g *geomCache) buildSurface(id int, angleScale float64, depth int) (Surface, error) {
	if depth > 8 {
		return nil, errors.New("surface nesting too deep")
	}
	e := g.f.Get(id)
	if e == nil {
		return nil, fmt.Errorf("missing surface #%d", id)
	}
	switch {
	case e.Type == "PLANE":
		f, err := g.placement(e.Arg(1))
		if err != nil {
			return nil, err
		}
		return &planeSurface{f: f}, nil
	case e.Type == "CYLINDRICAL_SURFACE":
		f, err := g.placement(e.Arg(1))
		if err != nil {
			return nil, err
		}
		return &cylinderSurface{f: f, r: e.Arg(2).AsFloat()}, nil
	case e.Type == "CONICAL_SURFACE":
		f, err := g.placement(e.Arg(1))
		if err != nil {
			return nil, err
		}
		ang := e.Arg(3).AsFloat() * angleScale
		if math.Abs(ang) >= math.Pi/2 {
			// Clearly in degrees despite the context.
			ang = e.Arg(3).AsFloat() * math.Pi / 180
		}
		t := math.Tan(ang)
		if math.Abs(t) < 1e-12 {
			return &cylinderSurface{f: f, r: e.Arg(2).AsFloat()}, nil
		}
		return &coneSurface{f: f, r: e.Arg(2).AsFloat(), tan: t}, nil
	case e.Type == "SPHERICAL_SURFACE":
		f, err := g.placement(e.Arg(1))
		if err != nil {
			return nil, err
		}
		return &sphereSurface{f: f, r: e.Arg(2).AsFloat()}, nil
	case e.Type == "TOROIDAL_SURFACE" || e.Type == "DEGENERATE_TOROIDAL_SURFACE":
		f, err := g.placement(e.Arg(1))
		if err != nil {
			return nil, err
		}
		return &torusSurface{f: f, R: e.Arg(2).AsFloat(), r: e.Arg(3).AsFloat()}, nil
	case e.Type == "SURFACE_OF_LINEAR_EXTRUSION":
		c, err := g.curve(e.Arg(1).Ref)
		if err != nil {
			return nil, err
		}
		d, ok := g.direction(e.Arg(2))
		if !ok || d.Len() == 0 {
			return nil, errors.New("bad extrusion vector")
		}
		return newExtrusion(c, d), nil
	case e.Type == "SURFACE_OF_REVOLUTION":
		c, err := g.curve(e.Arg(1).Ref)
		if err != nil {
			return nil, err
		}
		f, err := g.placement(e.Arg(2))
		if err != nil {
			return nil, err
		}
		return newRevolution(c, f.o, f.z), nil
	case e.Type == "OFFSET_SURFACE":
		b, err := g.buildSurface(e.Arg(1).Ref, angleScale, depth+1)
		if err != nil {
			return nil, err
		}
		return &offsetSurface{b: b, d: e.Arg(2).AsFloat()}, nil
	case e.Type == "RECTANGULAR_TRIMMED_SURFACE" || e.Type == "CURVE_BOUNDED_SURFACE":
		return g.buildSurface(e.Arg(1).Ref, angleScale, depth+1)
	case e.Is("B_SPLINE_SURFACE_WITH_KNOTS") || e.Is("BEZIER_SURFACE") || e.Is("UNIFORM_SURFACE") || e.Is("QUASI_UNIFORM_SURFACE"):
		return g.bsplineSurface(e)
	}
	return nil, fmt.Errorf("unsupported surface %s", entityTypeName(e))
}

func (g *geomCache) bsplineSurface(e *Entity) (Surface, error) {
	var pu, pv int
	var netV []Value
	if args, ok := e.PartArgs("B_SPLINE_SURFACE"); ok && e.Type == "" {
		if len(args) < 3 {
			return nil, errors.New("bad B_SPLINE_SURFACE")
		}
		pu, pv = int(args[0].AsFloat()), int(args[1].AsFloat())
		netV = args[2].AsList()
	} else {
		pu, pv = int(e.Arg(1).AsFloat()), int(e.Arg(2).AsFloat())
		netV = e.Arg(3).AsList()
	}
	net := make([][]Vec3, len(netV))
	for i, row := range netV {
		for _, v := range row.AsList() {
			p, err := g.point(v)
			if err != nil {
				return nil, err
			}
			net[i] = append(net[i], p)
		}
	}
	if len(net) == 0 || len(net[0]) == 0 {
		return nil, errors.New("empty control net")
	}
	var UK, VK []float64
	if e.Type == "" {
		if args, ok := e.PartArgs("B_SPLINE_SURFACE_WITH_KNOTS"); ok && len(args) >= 4 {
			UK = expandKnots(floats(args[2].AsList()), ints(args[0].AsList()))
			VK = expandKnots(floats(args[3].AsList()), ints(args[1].AsList()))
		}
	} else if e.Type == "B_SPLINE_SURFACE_WITH_KNOTS" {
		UK = expandKnots(floats(e.Arg(10).AsList()), ints(e.Arg(8).AsList()))
		VK = expandKnots(floats(e.Arg(11).AsList()), ints(e.Arg(9).AsList()))
	}
	if UK == nil {
		UK = implicitKnots(e, len(net), pu)
		VK = implicitKnots(e, len(net[0]), pv)
	}
	var w [][]float64
	if args, ok := e.PartArgs("RATIONAL_B_SPLINE_SURFACE"); ok && len(args) >= 1 {
		for _, row := range args[0].AsList() {
			w = append(w, floats(row.AsList()))
		}
		if len(w) != len(net) {
			return nil, errors.New("weight count mismatch")
		}
	}
	s, err := newBSplineSurface(pu, pv, net, w, UK, VK)
	if err != nil {
		return nil, err
	}
	// Precompute lazily-built data so the surface is safe for concurrent use.
	s.ensureGrid()
	s.metricScale()
	s.sizeCache = s.computeSize()
	return s, nil
}

// sampleCurveParams adaptively samples parameters of c over [ta, tb] so the
// chord error is below tol (if tol > 0).
func sampleCurveParams(c Curve, ta, tb, tol, maxAngle float64) []float64 {
	n := c.InitialSegments(ta, tb, tol, maxAngle)
	n = max(1, min(n, 4096))
	ts := make([]float64, 0, n+1)
	ps := make([]Vec3, 0, n+1)
	for i := 0; i <= n; i++ {
		t := ta + (tb-ta)*float64(i)/float64(n)
		p, _ := c.EvalD(t)
		ts = append(ts, t)
		ps = append(ps, p)
	}
	if tol <= 0 {
		return ts
	}
	out := []float64{ts[0]}
	var rec func(t0, t1 float64, p0, p1 Vec3, depth int)
	rec = func(t0, t1 float64, p0, p1 Vec3, depth int) {
		tm := (t0 + t1) / 2
		pm, _ := c.EvalD(tm)
		if depth < 8 && pm.Dist(p0.Lerp(p1, 0.5)) > tol {
			rec(t0, tm, p0, pm, depth+1)
			rec(tm, t1, pm, p1, depth+1)
			return
		}
		out = append(out, t1)
	}
	for i := 0; i < n; i++ {
		rec(ts[i], ts[i+1], ps[i], ps[i+1], 0)
	}
	return out
}
