package step

import (
	"errors"
	"math"
)

// findSpan returns the knot span index for parameter u (The NURBS Book A2.1).
// n is the index of the last control point.
func findSpan(n, p int, u float64, U []float64) int {
	// At the ends of the domain use the nearest non-empty span: repeated end
	// knots can make span n or p zero-length, where every basis function is 0.
	if u >= U[n+1] {
		for n > p && U[n] >= U[n+1] {
			n--
		}
		return n
	}
	if u <= U[p] {
		for p < n && U[p] >= U[p+1] {
			p++
		}
		return p
	}
	low, high := p, n+1
	mid := (low + high) / 2
	for u < U[mid] || u >= U[mid+1] {
		if u < U[mid] {
			high = mid
		} else {
			low = mid
		}
		mid = (low + high) / 2
	}
	return mid
}

// dersBasisFuns computes the non-zero basis functions and their first
// derivatives at u (The NURBS Book A2.3 with n=1). N and dN must have
// length p+1. left/right are scratch of length p+1.
func dersBasisFuns(span int, u float64, p int, U []float64, N, dN []float64) {
	// ndu[j*w+r] holds knot differences in the lower triangle and basis
	// function values in the upper triangle.
	var buf [16 * 16]float64
	var lbuf, rbuf [16]float64
	w := p + 1
	var ndu, left, right []float64
	if w <= 16 {
		ndu, left, right = buf[:w*w], lbuf[:w], rbuf[:w]
	} else {
		ndu, left, right = make([]float64, w*w), make([]float64, w), make([]float64, w)
	}
	ndu[0] = 1
	for j := 1; j <= p; j++ {
		left[j] = u - U[span+1-j]
		right[j] = U[span+j] - u
		saved := 0.0
		for r := 0; r < j; r++ {
			den := right[r+1] + left[j-r]
			ndu[j*w+r] = den
			temp := 0.0
			if den != 0 {
				temp = ndu[r*w+j-1] / den
			}
			ndu[r*w+j] = saved + right[r+1]*temp
			saved = left[j-r] * temp
		}
		ndu[j*w+j] = saved
	}
	for j := 0; j <= p; j++ {
		N[j] = ndu[j*w+p]
	}
	if p == 0 {
		dN[0] = 0
		return
	}
	for r := 0; r <= p; r++ {
		d := 0.0
		if r >= 1 {
			if den := ndu[p*w+r-1]; den != 0 {
				d += ndu[(r-1)*w+p-1] / den
			}
		}
		if r <= p-1 {
			if den := ndu[p*w+r]; den != 0 {
				d -= ndu[r*w+p-1] / den
			}
		}
		dN[r] = d * float64(p)
	}
}

// expandKnots expands STEP knots/multiplicities into a full knot vector.
func expandKnots(knots []float64, mults []int) []float64 {
	var out []float64
	for i, k := range knots {
		m := 1
		if i < len(mults) {
			m = mults[i]
		}
		for j := 0; j < m; j++ {
			out = append(out, k)
		}
	}
	return out
}

// fixKnots makes a knot vector compatible with the control point count by
// padding or trimming at the ends, which handles some writer quirks.
func fixKnots(U []float64, nCtrl, p int) ([]float64, error) {
	want := nCtrl + p + 1
	if len(U) == want {
		return U, nil
	}
	if len(U) == want+2 {
		// Some writers include extra end knots (OCC "periodic" style).
		return U[1 : len(U)-1], nil
	}
	return nil, errors.New("knot vector length mismatch")
}

// BSplineCurve is a (possibly rational) B-spline curve.
type BSplineCurve struct {
	P       int
	Ctrl    []Vec3
	W       []float64 // nil if polynomial
	U       []float64
	t0, t1  float64
	samples []curveSample
}

type curveSample struct {
	t float64
	p Vec3
}

func newBSplineCurve(p int, ctrl []Vec3, w []float64, U []float64) (*BSplineCurve, error) {
	if p < 1 || len(ctrl) < p+1 {
		return nil, errors.New("bad b-spline curve")
	}
	U, err := fixKnots(U, len(ctrl), p)
	if err != nil {
		return nil, err
	}
	c := &BSplineCurve{P: p, Ctrl: ctrl, W: w, U: U}
	c.t0 = U[p]
	c.t1 = U[len(ctrl)]
	if !(c.t1 > c.t0) {
		return nil, errors.New("degenerate b-spline curve domain")
	}
	c.ensureSamples()
	return c, nil
}

// Eval returns the point and derivative at t.
func (c *BSplineCurve) EvalD(t float64) (Vec3, Vec3) {
	t = math.Max(c.t0, math.Min(c.t1, t))
	n := len(c.Ctrl) - 1
	span := findSpan(n, c.P, t, c.U)
	var nb, db [16]float64
	N, dN := nb[:], db[:]
	if c.P+1 > len(nb) {
		N = make([]float64, c.P+1)
		dN = make([]float64, c.P+1)
	}
	dersBasisFuns(span, t, c.P, c.U, N, dN)
	var a, da Vec3
	w, dw := 0.0, 0.0
	for j := 0; j <= c.P; j++ {
		i := span - c.P + j
		wi := 1.0
		if c.W != nil {
			wi = c.W[i]
		}
		a = a.Add(c.Ctrl[i].Scale(N[j] * wi))
		da = da.Add(c.Ctrl[i].Scale(dN[j] * wi))
		w += N[j] * wi
		dw += dN[j] * wi
	}
	if w == 0 {
		return a, da
	}
	pt := a.Scale(1 / w)
	d := da.Sub(pt.Scale(dw)).Scale(1 / w)
	return pt, d
}

func (c *BSplineCurve) Eval(t float64) Vec3 {
	p, _ := c.EvalD(t)
	return p
}

func (c *BSplineCurve) Range() (float64, float64) { return c.t0, c.t1 }
func (c *BSplineCurve) Period() float64           { return 0 }

func (c *BSplineCurve) InitialSegments(ta, tb, tol, maxAngle float64) int {
	spans := 0
	for i := c.P; i < len(c.Ctrl); i++ {
		if c.U[i+1] > c.U[i] && c.U[i+1] > math.Min(ta, tb) && c.U[i] < math.Max(ta, tb) {
			spans++
		}
	}
	return max(1, spans) * max(2, c.P)
}

func (c *BSplineCurve) ensureSamples() {
	if c.samples != nil {
		return
	}
	n := min(400, max(16, (len(c.Ctrl)+1)*4))
	c.samples = make([]curveSample, n+1)
	for i := 0; i <= n; i++ {
		t := c.t0 + (c.t1-c.t0)*float64(i)/float64(n)
		c.samples[i] = curveSample{t, c.Eval(t)}
	}
}

func (c *BSplineCurve) Project(p Vec3) float64 {
	c.ensureSamples()
	best := 0
	bestD := math.Inf(1)
	for i, s := range c.samples {
		if d := s.p.Dist2(p); d < bestD {
			bestD = d
			best = i
		}
	}
	return c.refine(p, c.samples[best].t)
}

func (c *BSplineCurve) refine(p Vec3, t float64) float64 {
	for range 20 {
		q, d := c.EvalD(t)
		r := q.Sub(p)
		den := d.Dot(d)
		if den < 1e-300 {
			break
		}
		dt := -r.Dot(d) / den
		nt := math.Max(c.t0, math.Min(c.t1, t+dt))
		if math.Abs(nt-t) < 1e-12*(1+math.Abs(t)) {
			t = nt
			break
		}
		t = nt
	}
	return t
}

// BSplineSurface is a (possibly rational) B-spline surface.
type BSplineSurface struct {
	PU, PV    int
	NU, NV    int    // control point counts
	Ctrl      []Vec3 // NU*NV, index i*NV + j (i along u)
	W         []float64
	UK, VK    []float64
	u0, u1    float64
	v0, v1    float64
	periodU   float64
	periodV   float64
	grid      []Vec3
	gu, gv    int
	scaleU    float64
	scaleV    float64
	hasScale  bool
	sizeCache float64
}

func newBSplineSurface(pu, pv int, ctrl [][]Vec3, w [][]float64, UK, VK []float64) (*BSplineSurface, error) {
	nu := len(ctrl)
	if nu == 0 {
		return nil, errors.New("empty b-spline surface")
	}
	nv := len(ctrl[0])
	if pu < 1 || pv < 1 || nu < pu+1 || nv < pv+1 {
		return nil, errors.New("bad b-spline surface")
	}
	s := &BSplineSurface{PU: pu, PV: pv, NU: nu, NV: nv}
	s.Ctrl = make([]Vec3, nu*nv)
	if w != nil {
		s.W = make([]float64, nu*nv)
	}
	for i := range nu {
		if len(ctrl[i]) != nv {
			return nil, errors.New("ragged control net")
		}
		copy(s.Ctrl[i*nv:], ctrl[i])
		if w != nil {
			if len(w[i]) != nv {
				return nil, errors.New("ragged weights")
			}
			copy(s.W[i*nv:], w[i])
		}
	}
	var err error
	if s.UK, err = fixKnots(UK, nu, pu); err != nil {
		return nil, err
	}
	if s.VK, err = fixKnots(VK, nv, pv); err != nil {
		return nil, err
	}
	s.u0, s.u1 = s.UK[pu], s.UK[nu]
	s.v0, s.v1 = s.VK[pv], s.VK[nv]
	if !(s.u1 > s.u0) || !(s.v1 > s.v0) {
		return nil, errors.New("degenerate b-spline surface domain")
	}
	s.detectClosed()
	return s, nil
}

// detectClosed checks whether the surface wraps around in u or v.
func (s *BSplineSurface) detectClosed() {
	size := 0.0
	b := EmptyBox()
	for _, p := range s.Ctrl {
		b.Extend(p)
	}
	size = b.Diag()
	tol := size * 1e-6
	closedU, closedV := true, true
	for k := 0; k <= 4; k++ {
		t := float64(k) / 4
		v := s.v0 + (s.v1-s.v0)*t
		if s.evalRaw(s.u0, v).Dist(s.evalRaw(s.u1, v)) > tol {
			closedU = false
		}
		u := s.u0 + (s.u1-s.u0)*t
		if s.evalRaw(u, s.v0).Dist(s.evalRaw(u, s.v1)) > tol {
			closedV = false
		}
	}
	// A surface whose whole boundary collapses to a point is not "closed"
	// in the periodic sense; require the boundary curve to have extent.
	if closedU && s.evalRaw(s.u0, s.v0).Dist(s.evalRaw(s.u0, s.v1)) < tol*10 {
		closedU = false
	}
	if closedV && s.evalRaw(s.u0, s.v0).Dist(s.evalRaw(s.u1, s.v0)) < tol*10 {
		closedV = false
	}
	if closedU {
		s.periodU = s.u1 - s.u0
	}
	if closedV {
		s.periodV = s.v1 - s.v0
	}
}

func (s *BSplineSurface) wrap(u, v float64) (float64, float64) {
	if s.periodU > 0 {
		u = s.u0 + math.Mod(math.Mod(u-s.u0, s.periodU)+s.periodU, s.periodU)
	}
	if s.periodV > 0 {
		v = s.v0 + math.Mod(math.Mod(v-s.v0, s.periodV)+s.periodV, s.periodV)
	}
	return math.Max(s.u0, math.Min(s.u1, u)), math.Max(s.v0, math.Min(s.v1, v))
}

func (s *BSplineSurface) evalRaw(u, v float64) Vec3 {
	p, _, _ := s.evalD(u, v)
	return p
}

// evalD evaluates the point and first partial derivatives.
func (s *BSplineSurface) evalD(u, v float64) (Vec3, Vec3, Vec3) {
	u = math.Max(s.u0, math.Min(s.u1, u))
	v = math.Max(s.v0, math.Min(s.v1, v))
	su := findSpan(s.NU-1, s.PU, u, s.UK)
	sv := findSpan(s.NV-1, s.PV, v, s.VK)
	var nub, dub, nvb, dvb [16]float64
	Nu, dNu, Nv, dNv := nub[:], dub[:], nvb[:], dvb[:]
	if s.PU+1 > 16 {
		Nu, dNu = make([]float64, s.PU+1), make([]float64, s.PU+1)
	}
	if s.PV+1 > 16 {
		Nv, dNv = make([]float64, s.PV+1), make([]float64, s.PV+1)
	}
	dersBasisFuns(su, u, s.PU, s.UK, Nu, dNu)
	dersBasisFuns(sv, v, s.PV, s.VK, Nv, dNv)
	var A, Au, Av Vec3
	W, Wu, Wv := 0.0, 0.0, 0.0
	for a := 0; a <= s.PU; a++ {
		i := su - s.PU + a
		for b := 0; b <= s.PV; b++ {
			j := sv - s.PV + b
			k := i*s.NV + j
			w := 1.0
			if s.W != nil {
				w = s.W[k]
			}
			c := s.Ctrl[k]
			f := Nu[a] * Nv[b] * w
			fu := dNu[a] * Nv[b] * w
			fv := Nu[a] * dNv[b] * w
			A = A.Add(c.Scale(f))
			Au = Au.Add(c.Scale(fu))
			Av = Av.Add(c.Scale(fv))
			W += f
			Wu += fu
			Wv += fv
		}
	}
	if W == 0 {
		return A, Au, Av
	}
	P := A.Scale(1 / W)
	Pu := Au.Sub(P.Scale(Wu)).Scale(1 / W)
	Pv := Av.Sub(P.Scale(Wv)).Scale(1 / W)
	return P, Pu, Pv
}

func (s *BSplineSurface) Eval(u, v float64) Vec3 {
	u, v = s.wrap(u, v)
	return s.evalRaw(u, v)
}

func (s *BSplineSurface) Derivs(u, v float64) (Vec3, Vec3, Vec3) {
	u, v = s.wrap(u, v)
	return s.evalD(u, v)
}

func (s *BSplineSurface) Normal(u, v float64) Vec3 {
	_, du, dv := s.Derivs(u, v)
	return du.Cross(dv).Norm()
}

func (s *BSplineSurface) PeriodU() float64 { return s.periodU }
func (s *BSplineSurface) PeriodV() float64 { return s.periodV }

func (s *BSplineSurface) Domain() (float64, float64, float64, float64, bool) {
	return s.u0, s.u1, s.v0, s.v1, true
}

func (s *BSplineSurface) ensureGrid() {
	if s.grid != nil {
		return
	}
	s.gu = min(64, max(8, (s.NU-s.PU)*4+1))
	s.gv = min(64, max(8, (s.NV-s.PV)*4+1))
	s.grid = make([]Vec3, (s.gu+1)*(s.gv+1))
	for i := 0; i <= s.gu; i++ {
		u := s.u0 + (s.u1-s.u0)*float64(i)/float64(s.gu)
		for j := 0; j <= s.gv; j++ {
			v := s.v0 + (s.v1-s.v0)*float64(j)/float64(s.gv)
			s.grid[i*(s.gv+1)+j] = s.evalRaw(u, v)
		}
	}
}

func (s *BSplineSurface) Project(p Vec3, hint UV, hasHint bool) UV {
	if hasHint {
		uv, d := s.newton(p, hint)
		if d < 1e-4*(1+s.size()) {
			return uv
		}
	}
	s.ensureGrid()
	best, bestD := 0, math.Inf(1)
	for k, q := range s.grid {
		if d := q.Dist2(p); d < bestD {
			bestD = d
			best = k
		}
	}
	i, j := best/(s.gv+1), best%(s.gv+1)
	start := UV{
		s.u0 + (s.u1-s.u0)*float64(i)/float64(s.gu),
		s.v0 + (s.v1-s.v0)*float64(j)/float64(s.gv),
	}
	uv, _ := s.newton(p, start)
	return uv
}

func (s *BSplineSurface) size() float64 { return s.sizeCache }

func (s *BSplineSurface) computeSize() float64 {
	s.ensureGrid()
	b := EmptyBox()
	for _, q := range s.grid {
		b.Extend(q)
	}
	return b.Diag()
}

// newton projects p onto the surface with Gauss-Newton iterations starting
// at uv. It returns the parameters and the remaining distance.
func (s *BSplineSurface) newton(p Vec3, uv UV) (UV, float64) {
	u, v := uv.U, uv.V
	if s.periodU == 0 {
		u = math.Max(s.u0, math.Min(s.u1, u))
	}
	if s.periodV == 0 {
		v = math.Max(s.v0, math.Min(s.v1, v))
	}
	var dist float64
	for range 30 {
		q, du, dv := s.Derivs(u, v)
		r := q.Sub(p)
		dist = r.Len()
		a := du.Dot(du)
		b := du.Dot(dv)
		c := dv.Dot(dv)
		ru := r.Dot(du)
		rv := r.Dot(dv)
		det := a*c - b*b
		var stepU, stepV float64
		if math.Abs(det) < 1e-300 {
			// Degenerate (e.g. at a pole); move along whichever derivative exists.
			if a > 1e-300 {
				stepU = -ru / a
			}
			if c > 1e-300 {
				stepV = -rv / c
			}
		} else {
			stepU = -(c*ru - b*rv) / det
			stepV = -(a*rv - b*ru) / det
		}
		// Limit the step to a fraction of the domain to avoid wild jumps.
		maxU := (s.u1 - s.u0) * 0.25
		maxV := (s.v1 - s.v0) * 0.25
		stepU = math.Max(-maxU, math.Min(maxU, stepU))
		stepV = math.Max(-maxV, math.Min(maxV, stepV))
		nu, nv := u+stepU, v+stepV
		if s.periodU == 0 {
			nu = math.Max(s.u0, math.Min(s.u1, nu))
		}
		if s.periodV == 0 {
			nv = math.Max(s.v0, math.Min(s.v1, nv))
		}
		done := math.Abs(nu-u) < 1e-13*(s.u1-s.u0) && math.Abs(nv-v) < 1e-13*(s.v1-s.v0)
		u, v = nu, nv
		if done {
			break
		}
	}
	q := s.Eval(u, v)
	dist = q.Dist(p)
	return UV{u, v}, dist
}

func (s *BSplineSurface) metricScale() (float64, float64) {
	if s.hasScale {
		return s.scaleU, s.scaleV
	}
	var su, sv float64
	n := 0
	for i := 0; i <= 4; i++ {
		for j := 0; j <= 4; j++ {
			u := s.u0 + (s.u1-s.u0)*float64(i)/4
			v := s.v0 + (s.v1-s.v0)*float64(j)/4
			_, du, dv := s.evalD(u, v)
			su += du.Len()
			sv += dv.Len()
			n++
		}
	}
	s.scaleU = math.Max(su/float64(n), 1e-9)
	s.scaleV = math.Max(sv/float64(n), 1e-9)
	s.hasScale = true
	return s.scaleU, s.scaleV
}

func (s *BSplineSurface) Scale() (float64, float64) { return s.metricScale() }

func (s *BSplineSurface) Singular(u, v float64) (bool, bool) {
	_, du, dv := s.Derivs(u, v)
	su, sv := s.metricScale()
	return du.Len() < su*1e-6, dv.Len() < sv*1e-6
}

func (s *BSplineSurface) GridLines(tol, maxAngle float64, b UVBox) ([]float64, []float64) {
	return s.knotLines(s.UK, s.PU, s.NU, b.U0, b.U1, s.periodU), s.knotLines(s.VK, s.PV, s.NV, b.V0, b.V1, s.periodV)
}

// knotLines returns grid line parameters inside (lo, hi) with a few
// subdivisions per knot span.
func (s *BSplineSurface) knotLines(K []float64, p, n int, lo, hi, period float64) []float64 {
	var distinct []float64
	for i := p; i <= n; i++ {
		if len(distinct) == 0 || K[i] > distinct[len(distinct)-1] {
			distinct = append(distinct, K[i])
		}
	}
	sub := max(2, p+1)
	if len(distinct)*sub > 200 {
		sub = max(1, 200/len(distinct))
	}
	var base []float64
	for k := 0; k+1 < len(distinct); k++ {
		a, b := distinct[k], distinct[k+1]
		for j := 0; j < sub; j++ {
			base = append(base, a+(b-a)*float64(j)/float64(sub))
		}
	}
	base = append(base, distinct[len(distinct)-1])
	var out []float64
	if period > 0 {
		// Replicate across periods to cover unwrapped ranges.
		k0 := math.Floor((lo - distinct[0]) / period)
		k1 := math.Ceil((hi - distinct[0]) / period)
		for k := k0; k <= k1; k++ {
			for _, t := range base {
				t += k * period
				if t > lo && t < hi {
					out = append(out, t)
				}
			}
		}
		return out
	}
	for _, t := range base {
		if t > lo && t < hi {
			out = append(out, t)
		}
	}
	return out
}

func (s *BSplineSurface) IsPlane() bool { return false }
