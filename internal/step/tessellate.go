package step

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
)

// tessParams controls tessellation density.
type tessParams struct {
	tol      float64 // chordal tolerance in model units
	maxAngle float64 // maximum angle subtended by one segment (radians)
}

// faceMesh is the tessellation of a single face.
type faceMesh struct {
	pos  []Vec3
	nrm  []Vec3
	tris []int32
}

// bpt is a boundary point in parameter space with its exact 3D location.
type bpt struct {
	uv UV
	p  Vec3
}

// swappedSurface exchanges the roles of u and v.
type swappedSurface struct{ s Surface }

func (w swappedSurface) Eval(u, v float64) Vec3   { return w.s.Eval(v, u) }
func (w swappedSurface) Normal(u, v float64) Vec3 { return w.s.Normal(v, u) }
func (w swappedSurface) Project(p Vec3, h UV, ok bool) UV {
	r := w.s.Project(p, UV{h.V, h.U}, ok)
	return UV{r.V, r.U}
}
func (w swappedSurface) PeriodU() float64 { return w.s.PeriodV() }
func (w swappedSurface) PeriodV() float64 { return w.s.PeriodU() }
func (w swappedSurface) Domain() (float64, float64, float64, float64, bool) {
	u0, u1, v0, v1, ok := w.s.Domain()
	return v0, v1, u0, u1, ok
}
func (w swappedSurface) Scale() (float64, float64) {
	a, b := w.s.Scale()
	return b, a
}
func (w swappedSurface) Singular(u, v float64) (bool, bool) {
	a, b := w.s.Singular(v, u)
	return b, a
}
func (w swappedSurface) GridLines(tol, maxAngle float64, b UVBox) ([]float64, []float64) {
	us, vs := w.s.GridLines(tol, maxAngle, UVBox{b.V0, b.V1, b.U0, b.U1})
	return vs, us
}
func (w swappedSurface) IsPlane() bool { return w.s.IsPlane() }

var errFace = errors.New("face tessellation failed")

// errEmptyFace reports a face with no area (e.g. a degenerate sliver).
var errEmptyFace = errors.New("face has no area")

// tessellateFace triangulates a face given its surface, orientation and
// boundary loops (3D polylines, implicitly closed).
func tessellateFace(surf Surface, sense bool, loops [][]Vec3, prm tessParams) (*faceMesh, error) {
	// Clean loops: remove consecutive duplicates and drop degenerate ones.
	var clean [][]Vec3
	for _, l := range loops {
		l = dedupeLoop(l, prm.tol*1e-3)
		if len(l) >= 2 {
			clean = append(clean, l)
		}
	}
	if surf.IsPlane() {
		return tessellatePlanar(surf, sense, clean, prm)
	}
	fm, err := tessellateCurved(surf, sense, clean, prm, false)
	if err == nil {
		return fm, nil
	}
	if surf.PeriodV() > 0 || surf.PeriodU() > 0 {
		// Retry treating v as the primary periodic direction.
		if fm, err2 := tessellateCurved(swappedSurface{surf}, sense, clean, prm, true); err2 == nil {
			return fm, nil
		}
	}
	return tessellateProjected(surf, sense, clean, prm)
}

func dedupeLoop(l []Vec3, eps float64) []Vec3 {
	out := make([]Vec3, 0, len(l))
	for _, p := range l {
		if len(out) > 0 && out[len(out)-1].Dist(p) <= eps {
			continue
		}
		out = append(out, p)
	}
	for len(out) > 1 && out[0].Dist(out[len(out)-1]) <= eps {
		out = out[:len(out)-1]
	}
	return out
}

func tessellatePlanar(surf Surface, sense bool, loops [][]Vec3, prm tessParams) (*faceMesh, error) {
	var polys [][]bpt
	for _, l := range loops {
		if len(l) < 3 {
			continue
		}
		poly := make([]bpt, len(l))
		for i, p := range l {
			poly[i] = bpt{surf.Project(p, UV{}, false), p}
		}
		polys = append(polys, poly)
	}
	if len(polys) == 0 {
		return nil, fmt.Errorf("%w: no usable boundary loops", errFace)
	}
	n := surf.Normal(0, 0)
	if !sense {
		n = n.Scale(-1)
	}
	return triangulate(surf, n, polys, nil, 1, 1, prm, false)
}

// tessellateProjected is the fallback: project the loops onto a best fit
// plane and triangulate there.
func tessellateProjected(surf Surface, sense bool, loops [][]Vec3, prm tessParams) (*faceMesh, error) {
	if len(loops) == 0 {
		return nil, fmt.Errorf("%w: degenerate boundary", errFace)
	}
	// Newell normal of all loops.
	var nrm Vec3
	var c Vec3
	cnt := 0
	for _, l := range loops {
		for i := range l {
			a, b := l[i], l[(i+1)%len(l)]
			nrm.X += (a.Y - b.Y) * (a.Z + b.Z)
			nrm.Y += (a.Z - b.Z) * (a.X + b.X)
			nrm.Z += (a.X - b.X) * (a.Y + b.Y)
			c = c.Add(a)
			cnt++
		}
	}
	if nrm.Len() < 1e-20 {
		return nil, fmt.Errorf("%w: degenerate boundary", errFace)
	}
	z := nrm.Norm()
	x := z.AnyPerp()
	plane := &planeSurface{f: frame{o: c.Scale(1 / float64(cnt)), x: x, y: z.Cross(x), z: z}}
	fm, err := tessellatePlanar(plane, true, loops, prm)
	if err != nil {
		return nil, err
	}
	// Use the real surface normals where available.
	for i, p := range fm.pos {
		uv := surf.Project(p, UV{}, false)
		n := surf.Normal(uv.U, uv.V)
		if n.Len() > 0.5 {
			if !sense {
				n = n.Scale(-1)
			}
			fm.nrm[i] = n
		}
	}
	orientTriangles(fm)
	return fm, nil
}

// mapLoop maps a 3D loop into parameter space, unwrapping periodic
// directions and splitting singular points (poles) into two parameter
// points. It returns the parameter polyline and the winding counts.
func mapLoop(surf Surface, l []Vec3) ([]bpt, int, int) {
	pu, pv := surf.PeriodU(), surf.PeriodV()
	n := len(l)
	uvs := make([]UV, n)
	sing := make([]bool, n)
	for i, p := range l {
		uvs[i] = surf.Project(p, UV{}, false)
		su, sv := surf.Singular(uvs[i].U, uvs[i].V)
		sing[i] = su || sv
	}
	// Start at a non-singular point.
	start := 0
	for i := range sing {
		if !sing[i] {
			start = i
			break
		}
	}
	order := make([]int, n)
	for i := range order {
		order[i] = (start + i) % n
	}
	out := make([]bpt, 0, n+4)
	var ref UV
	haveRef := false
	for _, idx := range order {
		p := l[idx]
		var uv UV
		if haveRef {
			uv = surf.Project(p, ref, true)
			if pu > 0 {
				uv.U = nearPeriod(uv.U, ref.U, pu)
			}
			if pv > 0 {
				uv.V = nearPeriod(uv.V, ref.V, pv)
			}
		} else {
			uv = uvs[idx]
		}
		if sing[idx] {
			out = append(out, bpt{uv, p})
			continue
		}
		ref = uv
		haveRef = true
		out = append(out, bpt{uv, p})
	}
	// Resolve singular points: replace each with copies at the neighbouring
	// free parameter values.
	res := make([]bpt, 0, len(out)+4)
	m := len(out)
	for i := range out {
		su, sv := surf.Singular(out[i].uv.U, out[i].uv.V)
		if !su && !sv {
			res = append(res, out[i])
			continue
		}
		prev := out[(i-1+m)%m]
		next := out[(i+1)%m]
		a, b := out[i], out[i]
		if su {
			a.uv.U, b.uv.U = prev.uv.U, next.uv.U
			if i == m-1 && pu > 0 {
				b.uv.U = nearPeriod(next.uv.U, prev.uv.U, pu)
			}
		}
		if sv {
			a.uv.V, b.uv.V = prev.uv.V, next.uv.V
			if i == m-1 && pv > 0 {
				b.uv.V = nearPeriod(next.uv.V, prev.uv.V, pv)
			}
		}
		res = append(res, a)
		if b.uv != a.uv {
			res = append(res, b)
		}
	}
	ku, kv := 0, 0
	if len(res) > 0 {
		first, last := res[0].uv, res[len(res)-1].uv
		if pu > 0 {
			ku = int(math.Round((nearPeriod(first.U, last.U, pu) - first.U) / pu))
		}
		if pv > 0 {
			kv = int(math.Round((nearPeriod(first.V, last.V, pv) - first.V) / pv))
		}
	}
	return res, ku, kv
}

func tessellateCurved(surf Surface, sense bool, loops [][]Vec3, prm tessParams, swapped bool) (*faceMesh, error) {
	pu := surf.PeriodU()
	var closed, cross [][]bpt
	var crossK []int
	for _, l := range loops {
		poly, ku, kv := mapLoop(surf, l)
		if len(poly) < 2 {
			continue
		}
		switch {
		case ku == 0 && kv == 0:
			if len(poly) >= 3 {
				closed = append(closed, poly)
			}
		case kv == 0 && (ku == 1 || ku == -1):
			cross = append(cross, poly)
			crossK = append(crossK, ku)
		default:
			return nil, fmt.Errorf("%w: loop wraps in both parameter directions", errFace)
		}
	}
	gridV := func(a, b float64, box UVBox) []float64 {
		_, vs := surf.GridLines(prm.tol, prm.maxAngle, box)
		lo, hi := math.Min(a, b), math.Max(a, b)
		var out []float64
		for _, v := range vs {
			if v > lo && v < hi {
				out = append(out, v)
			}
		}
		if a > b {
			for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
				out[i], out[j] = out[j], out[i]
			}
		}
		return out
	}
	var polys [][]bpt
	switch len(cross) {
	case 0:
		if len(closed) == 0 {
			// No boundary at all: a complete closed surface (sphere/torus).
			u0, u1, v0, v1, ok := surf.Domain()
			if !ok || math.IsInf(v0, 0) || math.IsInf(v1, 0) || pu == 0 {
				return nil, fmt.Errorf("%w: unbounded closed surface", errFace)
			}
			polys = append(polys, domainRect(surf, u0, u1, v0, v1, prm))
		} else {
			polys = closed
			if pu > 0 {
				shiftIntoRange(polys[1:], polys[0], pu)
			}
		}
	case 1:
		poly := cross[0]
		k := crossK[0]
		s := poly[0].uv.U
		end := poly[0]
		end.uv.U += float64(k) * pu
		poly = append(poly, end)
		_, _, v0, v1, _ := surf.Domain()
		side := k
		if !sense {
			side = -side
		}
		if swapped {
			side = -side
		}
		vb := v1
		if side < 0 {
			vb = v0
		}
		// Prefer a finite singular bound.
		isSing := func(v float64) bool {
			if math.IsInf(v, 0) {
				return false
			}
			a, _ := surf.Singular(s, v)
			return a
		}
		if !isSing(vb) {
			alt := v0
			if vb == v0 {
				alt = v1
			}
			if isSing(alt) {
				vb = alt
			} else if math.IsInf(vb, 0) {
				return nil, fmt.Errorf("%w: cannot close loop at a pole", errFace)
			}
		}
		box := polyBox(poly)
		box.V0 = math.Min(box.V0, vb)
		box.V1 = math.Max(box.V1, vb)
		ue := s + float64(k)*pu
		for _, v := range gridV(end.uv.V, vb, box) {
			poly = append(poly, bpt{UV{ue, v}, surf.Eval(ue, v)})
		}
		// Along the pole line from ue back to s.
		us, _ := surf.GridLines(prm.tol, prm.maxAngle, UVBox{math.Min(s, ue), math.Max(s, ue), box.V0, box.V1})
		poly = append(poly, bpt{UV{ue, vb}, surf.Eval(ue, vb)})
		if k > 0 {
			for _, u := range slices.Backward(us) {
				poly = append(poly, bpt{UV{u, vb}, surf.Eval(u, vb)})
			}
		} else {
			for _, u := range us {
				poly = append(poly, bpt{UV{u, vb}, surf.Eval(u, vb)})
			}
		}
		poly = append(poly, bpt{UV{s, vb}, surf.Eval(s, vb)})
		for _, v := range gridV(vb, poly[0].uv.V, box) {
			poly = append(poly, bpt{UV{s, v}, surf.Eval(s, v)})
		}
		polys = append(polys, poly)
		shiftIntoRange(closed, poly, pu)
		polys = append(polys, closed...)
	case 2:
		a, b := cross[0], cross[1]
		ka, kb := crossK[0], crossK[1]
		if ka == kb {
			b = reverseLoop(b, pu)
			kb = -kb
		}
		// Choose the cut position so it avoids the holes if possible.
		a = rotateAvoidingHoles(a, closed, pu)
		s := a[0].uv.U
		endA := a[0]
		endA.uv.U += float64(ka) * pu
		a = append(a, endA)
		bb, ok := cutLoopAt(b, kb, s, pu)
		if !ok {
			return nil, fmt.Errorf("%w: cannot cut loop at seam", errFace)
		}
		// Shift b so it starts where a ends.
		shift := nearPeriod(bb[0].uv.U, endA.uv.U, pu) - bb[0].uv.U
		for i := range bb {
			bb[i].uv.U += shift
		}
		poly := append([]bpt{}, a...)
		box := polyBox(a)
		bbox := polyBox(bb)
		box.V0 = math.Min(box.V0, bbox.V0)
		box.V1 = math.Max(box.V1, bbox.V1)
		for _, v := range gridV(endA.uv.V, bb[0].uv.V, box) {
			poly = append(poly, bpt{UV{endA.uv.U, v}, surf.Eval(endA.uv.U, v)})
		}
		poly = append(poly, bb...)
		last := bb[len(bb)-1]
		for _, v := range gridV(last.uv.V, a[0].uv.V, box) {
			poly = append(poly, bpt{UV{s, v}, surf.Eval(s, v)})
		}
		polys = append(polys, poly)
		shiftIntoRange(closed, poly, pu)
		polys = append(polys, closed...)
	default:
		return nil, fmt.Errorf("%w: too many seam-crossing loops", errFace)
	}
	su, sv := faceScale(surf, polys)
	return triangulate(surf, Vec3{}, polys, &sense, su, sv, prm, true)
}

func domainRect(surf Surface, u0, u1, v0, v1 float64, prm tessParams) []bpt {
	var out []bpt
	box := UVBox{u0, u1, v0, v1}
	us, vs := surf.GridLines(prm.tol, prm.maxAngle, box)
	add := func(u, v float64) { out = append(out, bpt{UV{u, v}, surf.Eval(u, v)}) }
	add(u0, v0)
	for _, u := range us {
		add(u, v0)
	}
	add(u1, v0)
	for _, v := range vs {
		add(u1, v)
	}
	add(u1, v1)
	for _, u := range slices.Backward(us) {
		add(u, v1)
	}
	add(u0, v1)
	for _, v := range slices.Backward(vs) {
		add(u0, v)
	}
	return out
}

func polyBox(p []bpt) UVBox {
	b := UVBox{math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)}
	for _, q := range p {
		b.U0 = math.Min(b.U0, q.uv.U)
		b.U1 = math.Max(b.U1, q.uv.U)
		b.V0 = math.Min(b.V0, q.uv.V)
		b.V1 = math.Max(b.V1, q.uv.V)
	}
	return b
}

// shiftIntoRange moves closed loops by whole periods so they lie within the
// u range of the outer polygon.
func shiftIntoRange(loops [][]bpt, outer []bpt, period float64) {
	if period <= 0 {
		return
	}
	ob := polyBox(outer)
	mid := (ob.U0 + ob.U1) / 2
	for _, l := range loops {
		lb := polyBox(l)
		c := (lb.U0 + lb.U1) / 2
		shift := nearPeriod(c, mid, period) - c
		if shift != 0 {
			for i := range l {
				l[i].uv.U += shift
			}
		}
	}
}

func reverseLoop(l []bpt, period float64) []bpt {
	out := make([]bpt, len(l))
	for i := range l {
		out[i] = l[len(l)-1-i]
	}
	return out
}

// rotateAvoidingHoles rotates loop a (keeping it unwrapped) so its first
// point's u does not fall inside the u extent of any hole.
func rotateAvoidingHoles(a []bpt, holes [][]bpt, period float64) []bpt {
	if len(holes) == 0 {
		return a
	}
	inHole := func(u float64) bool {
		for _, h := range holes {
			hb := polyBox(h)
			uu := nearPeriod(u, (hb.U0+hb.U1)/2, period)
			if uu >= hb.U0 && uu <= hb.U1 {
				return true
			}
		}
		return false
	}
	if !inHole(a[0].uv.U) {
		return a
	}
	for i := 1; i < len(a); i++ {
		if !inHole(a[i].uv.U) {
			out := make([]bpt, 0, len(a))
			out = append(out, a[i:]...)
			k := a[len(a)-1].uv.U - a[0].uv.U
			_ = k
			// Points wrapped from the start continue the unwrapped sequence.
			last := a[len(a)-1].uv.U
			for _, q := range a[:i] {
				q.uv.U = nearPeriod(q.uv.U, last, period)
				last = q.uv.U
				out = append(out, q)
			}
			return out
		}
	}
	return a
}

// cutLoopAt rotates a crossing loop to start and end where it crosses
// u = s (mod period), returning the unwrapped open polyline.
func cutLoopAt(l []bpt, k int, s, period float64) ([]bpt, bool) {
	n := len(l)
	ext := make([]bpt, n+1)
	copy(ext, l)
	ext[n] = l[0]
	ext[n].uv.U += float64(k) * period
	for i := range n {
		a, b := ext[i], ext[i+1]
		lo, hi := math.Min(a.uv.U, b.uv.U), math.Max(a.uv.U, b.uv.U)
		m := math.Ceil((lo - s) / period)
		uc := s + m*period
		if uc > hi || hi == lo {
			continue
		}
		t := (uc - a.uv.U) / (b.uv.U - a.uv.U)
		x := bpt{UV{uc, a.uv.V + (b.uv.V-a.uv.V)*t}, a.p.Lerp(b.p, t)}
		out := []bpt{x}
		for j := i + 1; j <= n; j++ {
			out = append(out, ext[j])
		}
		for j := 1; j <= i; j++ {
			q := ext[j]
			q.uv.U += float64(k) * period
			out = append(out, q)
		}
		xe := x
		xe.uv.U += float64(k) * period
		if out[len(out)-1].p.Dist(xe.p) > 0 || out[len(out)-1].uv != xe.uv {
			out = append(out, xe)
		}
		// Drop a duplicate of the crossing point right after the start.
		if len(out) > 1 && out[1].uv == x.uv {
			out = append(out[:1], out[2:]...)
		}
		return out, true
	}
	return nil, false
}

// segGrid is a bucket grid of segments for proximity queries.
type segGrid struct {
	x0, y0, cell float64
	nx, ny       int
	cells        [][]int32
	ax, ay       []float64
	bx, by       []float64
}

func newSegGrid(box UVBox, cell float64) *segGrid {
	g := &segGrid{x0: box.U0, y0: box.V0, cell: cell}
	g.nx = max(1, min(512, int(math.Ceil((box.U1-box.U0)/cell))+1))
	g.ny = max(1, min(512, int(math.Ceil((box.V1-box.V0)/cell))+1))
	g.cell = math.Max(cell, math.Max((box.U1-box.U0)/float64(g.nx-1+1), (box.V1-box.V0)/float64(g.ny-1+1)))
	g.cells = make([][]int32, g.nx*g.ny)
	return g
}

func (g *segGrid) idx(x, y float64) (int, int) {
	i := int((x - g.x0) / g.cell)
	j := int((y - g.y0) / g.cell)
	return max(0, min(g.nx-1, i)), max(0, min(g.ny-1, j))
}

func (g *segGrid) add(ax, ay, bx, by float64) {
	k := int32(len(g.ax))
	g.ax = append(g.ax, ax)
	g.ay = append(g.ay, ay)
	g.bx = append(g.bx, bx)
	g.by = append(g.by, by)
	i0, j0 := g.idx(math.Min(ax, bx), math.Min(ay, by))
	i1, j1 := g.idx(math.Max(ax, bx), math.Max(ay, by))
	for i := i0; i <= i1; i++ {
		for j := j0; j <= j1; j++ {
			g.cells[j*g.nx+i] = append(g.cells[j*g.nx+i], k)
		}
	}
}

// near reports whether any segment is within d of (x, y).
func (g *segGrid) near(x, y, d float64) bool {
	i0, j0 := g.idx(x-d, y-d)
	i1, j1 := g.idx(x+d, y+d)
	d2 := d * d
	for i := i0; i <= i1; i++ {
		for j := j0; j <= j1; j++ {
			for _, k := range g.cells[j*g.nx+i] {
				if segDist2(x, y, g.ax[k], g.ay[k], g.bx[k], g.by[k]) < d2 {
					return true
				}
			}
		}
	}
	return false
}

func segDist2(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	den := dx*dx + dy*dy
	t := 0.0
	if den > 0 {
		t = math.Max(0, math.Min(1, ((px-ax)*dx+(py-ay)*dy)/den))
	}
	ex, ey := ax+dx*t-px, ay+dy*t-py
	return ex*ex + ey*ey
}

// triangulate builds a constrained triangulation of the polygons in scaled
// parameter space, adds interior points for curved surfaces and maps the
// result to 3D. If fixedNormal is non-zero it is used for all vertices.
func triangulate(surf Surface, fixedNormal Vec3, polys [][]bpt, sense *bool, su, sv float64, prm tessParams, curved bool) (*faceMesh, error) {
	box := UVBox{math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)}
	total := 0
	for _, p := range polys {
		b := polyBox(p)
		box.U0 = math.Min(box.U0, b.U0)
		box.U1 = math.Max(box.U1, b.U1)
		box.V0 = math.Min(box.V0, b.V0)
		box.V1 = math.Max(box.V1, b.V1)
		total += len(p)
	}
	if total < 3 || math.IsInf(box.U0, 0) || math.IsNaN(box.U0) || math.IsNaN(box.V0) {
		return nil, fmt.Errorf("%w: invalid parameter range", errFace)
	}
	sbox := UVBox{box.U0 * su, box.U1 * su, box.V0 * sv, box.V1 * sv}
	c := newCDT(sbox)
	vert3D := map[int]Vec3{}
	vertUV := map[int]UV{}
	ids := make([][]int, len(polys))
	for pi, p := range polys {
		ids[pi] = make([]int, len(p))
		for i, q := range p {
			id := c.addPoint(q.uv.U*su, q.uv.V*sv)
			ids[pi][i] = id
			if _, ok := vert3D[id]; !ok {
				vert3D[id] = q.p
				vertUV[id] = q.uv
			}
		}
	}
	if curved {
		us, vs := surf.GridLines(prm.tol, prm.maxAngle, box)
		if len(us) > 0 && len(vs) > 0 {
			// Thin out very dense grids.
			for len(us)*len(vs) > 20000 {
				if len(us) > len(vs) {
					us = thin(us)
				} else {
					vs = thin(vs)
				}
			}
			minStep := math.Inf(1)
			for i := 1; i < len(us); i++ {
				minStep = math.Min(minStep, (us[i]-us[i-1])*su)
			}
			for i := 1; i < len(vs); i++ {
				minStep = math.Min(minStep, (vs[i]-vs[i-1])*sv)
			}
			if len(us) > 0 {
				minStep = math.Min(minStep, math.Min(us[0]-box.U0, box.U1-us[len(us)-1])*su*2)
			}
			if math.IsInf(minStep, 0) || minStep <= 0 {
				minStep = math.Max(sbox.U1-sbox.U0, sbox.V1-sbox.V0) / 10
			}
			g := newSegGrid(sbox, minStep)
			for _, p := range polys {
				for i := range p {
					a, b := p[i], p[(i+1)%len(p)]
					g.add(a.uv.U*su, a.uv.V*sv, b.uv.U*su, b.uv.V*sv)
				}
			}
			for _, v := range vs {
				xs := scanline(polys, v, su, sv)
				for _, u := range us {
					x := u * su
					// Even-odd inside test using the row crossings.
					k := sort.SearchFloat64s(xs, x)
					if k%2 == 0 {
						continue
					}
					if g.near(x, v*sv, minStep*0.3) {
						continue
					}
					id := c.addPoint(x, v*sv)
					if _, ok := vertUV[id]; !ok {
						vertUV[id] = UV{u, v}
					}
				}
			}
		}
	}
	for pi, p := range ids {
		for i := range p {
			if err := c.addConstraint(p[i], p[(i+1)%len(p)]); err != nil {
				return nil, err
			}
		}
		_ = pi
	}
	inside := c.inside()
	if curved {
		// Refine triangles whose centroid deviates from the surface.
		for range 6 {
			var add []UV
			for t, in := range inside {
				if !in {
					continue
				}
				tr := c.tris[t].v
				// Singular vertices (poles, apexes) have an arbitrary free
				// coordinate; use the mean of the other vertices instead.
				var sumU, sumV, cv, cu float64
				var cp Vec3
				for _, v := range tr {
					uv := vertUV[v]
					su, sv := surf.Singular(uv.U, uv.V)
					if !su {
						sumU += uv.U
						cu++
					}
					if !sv {
						sumV += uv.V
						cv++
					}
					cp = cp.Add(vertexPos(surf, vert3D, uv, v).Scale(1.0 / 3))
				}
				if cu == 0 || cv == 0 {
					continue
				}
				cuv := UV{sumU / cu, sumV / cv}
				if surf.Eval(cuv.U, cuv.V).Dist(cp) > prm.tol {
					add = append(add, cuv)
				}
			}
			if len(add) == 0 || len(c.px) > 60000 {
				break
			}
			for _, uv := range add {
				id := c.addPoint(uv.U*su, uv.V*sv)
				if _, ok := vertUV[id]; !ok {
					vertUV[id] = uv
				}
			}
			inside = c.inside()
		}
	}
	fm := &faceMesh{}
	remap := map[int]int32{}
	for t, in := range inside {
		if !in {
			continue
		}
		tr := c.tris[t].v
		var idx [3]int32
		for k, v := range tr {
			if r, ok := remap[v]; ok {
				idx[k] = r
				continue
			}
			uv, ok := vertUV[v]
			if !ok {
				x, y := c.point(v)
				uv = UV{x / su, y / sv}
				vertUV[v] = uv
			}
			p := vertexPos(surf, vert3D, uv, v)
			var n Vec3
			if fixedNormal.Len() > 0 {
				n = fixedNormal
			} else {
				n = surf.Normal(uv.U, uv.V)
				if sense != nil && !*sense {
					n = n.Scale(-1)
				}
			}
			r := int32(len(fm.pos))
			fm.pos = append(fm.pos, p)
			fm.nrm = append(fm.nrm, n)
			remap[v] = r
			idx[k] = r
		}
		fm.tris = append(fm.tris, idx[0], idx[1], idx[2])
	}
	if len(fm.tris) == 0 {
		return nil, errEmptyFace
	}
	orientTriangles(fm)
	return fm, nil
}

func thin(xs []float64) []float64 {
	out := xs[:0:0]
	for i := 0; i < len(xs); i += 2 {
		out = append(out, xs[i])
	}
	return out
}

func vertexPos(surf Surface, fixed map[int]Vec3, uv UV, v int) Vec3 {
	if p, ok := fixed[v]; ok {
		return p
	}
	return surf.Eval(uv.U, uv.V)
}

// scanline returns the sorted x coordinates where the polygons cross the
// horizontal line at parameter v.
func scanline(polys [][]bpt, v, su, sv float64) []float64 {
	var xs []float64
	for _, p := range polys {
		for i := range p {
			a, b := p[i].uv, p[(i+1)%len(p)].uv
			if (a.V > v) == (b.V > v) {
				continue
			}
			t := (v - a.V) / (b.V - a.V)
			xs = append(xs, (a.U+(b.U-a.U)*t)*su)
		}
	}
	sort.Float64s(xs)
	return xs
}

// orientTriangles makes each triangle's winding agree with its vertex
// normals, fills in missing normals and drops degenerate triangles.
func orientTriangles(fm *faceMesh) {
	out := fm.tris[:0]
	var acc []Vec3
	for i := 0; i+2 < len(fm.tris); i += 3 {
		a, b, c := fm.tris[i], fm.tris[i+1], fm.tris[i+2]
		g := fm.pos[b].Sub(fm.pos[a]).Cross(fm.pos[c].Sub(fm.pos[a]))
		gl := g.Len()
		if gl < 1e-24 || math.IsNaN(gl) {
			continue
		}
		ref := fm.nrm[a].Add(fm.nrm[b]).Add(fm.nrm[c])
		if ref.Dot(g) < 0 {
			b, c = c, b
			g = g.Scale(-1)
		}
		out = append(out, a, b, c)
		for _, v := range [3]int32{a, b, c} {
			if fm.nrm[v].Len() < 0.5 {
				if acc == nil {
					acc = make([]Vec3, len(fm.pos))
				}
				acc[v] = acc[v].Add(g)
			}
		}
	}
	fm.tris = out
	for i := range fm.nrm {
		if fm.nrm[i].Len() < 0.5 {
			if acc != nil {
				fm.nrm[i] = acc[i].Norm()
			}
		}
	}
}

// faceScale estimates the average magnitudes of dS/du and dS/dv over the
// face's boundary so parameter space can be made roughly isotropic.
func faceScale(surf Surface, polys [][]bpt) (float64, float64) {
	box := UVBox{math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)}
	n := 0
	for _, p := range polys {
		b := polyBox(p)
		box.U0 = math.Min(box.U0, b.U0)
		box.U1 = math.Max(box.U1, b.U1)
		box.V0 = math.Min(box.V0, b.V0)
		box.V1 = math.Max(box.V1, b.V1)
		n += len(p)
	}
	du := math.Max((box.U1-box.U0)*1e-3, 1e-9)
	dv := math.Max((box.V1-box.V0)*1e-3, 1e-9)
	step := max(1, n/200)
	var su, sv float64
	cnt := 0
	k := 0
	for _, p := range polys {
		for _, q := range p {
			k++
			if k%step != 0 {
				continue
			}
			u, v := q.uv.U, q.uv.V
			su += surf.Eval(u+du, v).Dist(surf.Eval(u-du, v)) / (2 * du)
			sv += surf.Eval(u, v+dv).Dist(surf.Eval(u, v-dv)) / (2 * dv)
			cnt++
		}
	}
	fu, fv := surf.Scale()
	if cnt > 0 {
		su /= float64(cnt)
		sv /= float64(cnt)
	}
	if !(su > 1e-12) || math.IsInf(su, 0) {
		su = fu
	}
	if !(sv > 1e-12) || math.IsInf(sv, 0) {
		sv = fv
	}
	return su, sv
}
