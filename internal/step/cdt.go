package step

import (
	"errors"
	"math"
)

// cdt is an incremental constrained Delaunay triangulation in 2D.
//
// Vertices 0..2 are the corners of a super triangle; user vertices follow.
// Each triangle stores its vertices counter-clockwise, the neighbour across
// the edge opposite each vertex, and whether that edge is constrained.
type cdt struct {
	px, py []float64
	tris   []cdtTri
	vt     []int // vertex -> some incident triangle
	last   int

	// normalisation
	ox, oy, inv float64

	ccount map[[2]int]int // constraint multiplicity per edge
}

type cdtTri struct {
	v [3]int
	n [3]int
	c [3]bool
}

const cdtEps = 1e-13

var errCDT = errors.New("cdt: failed to insert constraint")

func newCDT(b UVBox) *cdt {
	c := &cdt{ccount: map[[2]int]int{}}
	w := math.Max(b.U1-b.U0, b.V1-b.V0)
	if !(w > 0) {
		w = 1
	}
	c.ox, c.oy, c.inv = b.U0, b.V0, 1/w
	c.px = append(c.px, -100, 200, -100)
	c.py = append(c.py, -100, -100, 200)
	c.vt = append(c.vt, 0, 0, 0)
	c.tris = append(c.tris, cdtTri{v: [3]int{0, 1, 2}, n: [3]int{-1, -1, -1}})
	return c
}

func (c *cdt) orient(a, b, p int) float64 {
	return (c.px[b]-c.px[a])*(c.py[p]-c.py[a]) - (c.py[b]-c.py[a])*(c.px[p]-c.px[a])
}

func (c *cdt) orientXY(a, b int, x, y float64) float64 {
	return (c.px[b]-c.px[a])*(y-c.py[a]) - (c.py[b]-c.py[a])*(x-c.px[a])
}

// inCircle reports whether d is strictly inside the circumcircle of the
// counter-clockwise triangle a, b, c.
func (c *cdt) inCircle(a, b, cc, d int) bool {
	adx, ady := c.px[a]-c.px[d], c.py[a]-c.py[d]
	bdx, bdy := c.px[b]-c.px[d], c.py[b]-c.py[d]
	cdx, cdy := c.px[cc]-c.px[d], c.py[cc]-c.py[d]
	ad := adx*adx + ady*ady
	bd := bdx*bdx + bdy*bdy
	cd := cdx*cdx + cdy*cdy
	det := adx*(bdy*cd-bd*cdy) - ady*(bdx*cd-bd*cdx) + ad*(bdx*cdy-bdy*cdx)
	return det > 1e-12
}

func (c *cdt) setTri(t int, tr cdtTri) {
	c.tris[t] = tr
	for _, v := range tr.v {
		c.vt[v] = t
	}
}

func (c *cdt) newTri(tr cdtTri) int {
	c.tris = append(c.tris, tr)
	t := len(c.tris) - 1
	for _, v := range tr.v {
		c.vt[v] = t
	}
	return t
}

func (c *cdt) replaceNeighbor(t, old, nw int) {
	if t < 0 {
		return
	}
	for i := range 3 {
		if c.tris[t].n[i] == old {
			c.tris[t].n[i] = nw
			return
		}
	}
}

// locate finds the triangle containing (x, y). edge is the index of the
// edge the point lies on, or -1.
func (c *cdt) locate(x, y float64) (int, int) {
	t := c.last
	if t < 0 || t >= len(c.tris) {
		t = 0
	}
	for steps := 0; steps < 4*len(c.tris)+100; steps++ {
		tr := &c.tris[t]
		moved := false
		onEdge := -1
		for k := range 3 {
			i := (k + steps) % 3
			a, b := tr.v[(i+1)%3], tr.v[(i+2)%3]
			o := c.orientXY(a, b, x, y)
			if o < -cdtEps {
				if tr.n[i] >= 0 {
					t = tr.n[i]
					moved = true
					break
				}
			} else if o <= cdtEps {
				onEdge = i
			}
		}
		if !moved {
			return t, onEdge
		}
	}
	// Walk failed (degenerate geometry); fall back to a linear search.
	for t := range c.tris {
		tr := &c.tris[t]
		inside := true
		onEdge := -1
		for i := range 3 {
			o := c.orientXY(tr.v[(i+1)%3], tr.v[(i+2)%3], x, y)
			if o < -cdtEps {
				inside = false
				break
			} else if o <= cdtEps {
				onEdge = i
			}
		}
		if inside {
			return t, onEdge
		}
	}
	return 0, -1
}

// addPoint inserts a point (in user coordinates) and returns its vertex id.
// Points coinciding with an existing vertex return that vertex.
func (c *cdt) addPoint(u, v float64) int {
	x := (u - c.ox) * c.inv
	y := (v - c.oy) * c.inv
	t, e := c.locate(x, y)
	tr := c.tris[t]
	for _, vi := range tr.v {
		if math.Abs(c.px[vi]-x) < 1e-11 && math.Abs(c.py[vi]-y) < 1e-11 {
			return vi
		}
	}
	p := len(c.px)
	c.px = append(c.px, x)
	c.py = append(c.py, y)
	c.vt = append(c.vt, t)
	if e >= 0 && tr.n[e] >= 0 {
		c.splitEdge(t, e, p)
	} else {
		c.splitTri(t, p)
	}
	return p
}

func (c *cdt) splitTri(t, p int) {
	tr := c.tris[t]
	a, b, cc := tr.v[0], tr.v[1], tr.v[2]
	na, nb, nc := tr.n[0], tr.n[1], tr.n[2]
	ca, cb, ccn := tr.c[0], tr.c[1], tr.c[2]
	t1 := len(c.tris)
	t2 := t1 + 1
	c.tris = append(c.tris, cdtTri{}, cdtTri{})
	c.setTri(t, cdtTri{v: [3]int{a, b, p}, n: [3]int{t1, t2, nc}, c: [3]bool{false, false, ccn}})
	c.setTri(t1, cdtTri{v: [3]int{b, cc, p}, n: [3]int{t2, t, na}, c: [3]bool{false, false, ca}})
	c.setTri(t2, cdtTri{v: [3]int{cc, a, p}, n: [3]int{t, t1, nb}, c: [3]bool{false, false, cb}})
	c.replaceNeighbor(na, t, t1)
	c.replaceNeighbor(nb, t, t2)
	c.last = t
	c.legalize(t, 2)
	c.legalize(t1, 2)
	c.legalize(t2, 2)
}

func (c *cdt) splitEdge(t, e, p int) {
	tr := c.tris[t]
	a, b, cc := tr.v[e], tr.v[(e+1)%3], tr.v[(e+2)%3]
	u := tr.n[e]
	nb, nc := tr.n[(e+1)%3], tr.n[(e+2)%3]
	cb, ccn := tr.c[(e+1)%3], tr.c[(e+2)%3]
	cbc := tr.c[e]
	ut := c.tris[u]
	j := 0
	for j = 0; j < 3; j++ {
		if ut.n[j] == t {
			break
		}
	}
	d := ut.v[j]
	// In u: v[j]=d, v[j+1]=c, v[j+2]=b.
	ud1, ud2 := ut.n[(j+1)%3], ut.n[(j+2)%3]
	cd1, cd2 := ut.c[(j+1)%3], ut.c[(j+2)%3]
	T1, U1 := t, u
	T2 := len(c.tris)
	U2 := T2 + 1
	c.tris = append(c.tris, cdtTri{}, cdtTri{})
	c.setTri(T1, cdtTri{v: [3]int{a, b, p}, n: [3]int{U1, T2, nc}, c: [3]bool{cbc, false, ccn}})
	c.setTri(T2, cdtTri{v: [3]int{a, p, cc}, n: [3]int{U2, nb, T1}, c: [3]bool{cbc, cb, false}})
	c.setTri(U1, cdtTri{v: [3]int{d, p, b}, n: [3]int{T1, ud1, U2}, c: [3]bool{cbc, cd1, false}})
	c.setTri(U2, cdtTri{v: [3]int{d, cc, p}, n: [3]int{T2, U1, ud2}, c: [3]bool{cbc, false, cd2}})
	c.replaceNeighbor(nb, t, T2)
	c.replaceNeighbor(ud2, u, U2)
	if cbc {
		// The constrained edge b-c is now b-p and p-c.
		k := edgeKey(b, cc)
		if n, ok := c.ccount[k]; ok {
			delete(c.ccount, k)
			c.ccount[edgeKey(b, p)] += n
			c.ccount[edgeKey(p, cc)] += n
		}
	}
	c.last = T1
	c.legalize(T1, 2)
	c.legalize(T2, 1)
	c.legalize(U1, 1)
	c.legalize(U2, 2)
}

// flip flips the edge opposite vertex i of triangle t. It returns the two
// new triangles; in both, index 0 is the vertex t.v[i].
func (c *cdt) flip(t, i int) (int, int) {
	tr := c.tris[t]
	p, b, cc := tr.v[i], tr.v[(i+1)%3], tr.v[(i+2)%3]
	u := tr.n[i]
	tnb, tnc := tr.n[(i+1)%3], tr.n[(i+2)%3]
	tcb, tcc := tr.c[(i+1)%3], tr.c[(i+2)%3]
	ut := c.tris[u]
	j := 0
	for j = 0; j < 3; j++ {
		if ut.n[j] == t {
			break
		}
	}
	d := ut.v[j]
	unc, unb := ut.n[(j+1)%3], ut.n[(j+2)%3]
	ucc, ucb := ut.c[(j+1)%3], ut.c[(j+2)%3]
	c.setTri(t, cdtTri{v: [3]int{p, b, d}, n: [3]int{unc, u, tnc}, c: [3]bool{ucc, false, tcc}})
	c.setTri(u, cdtTri{v: [3]int{p, d, cc}, n: [3]int{unb, tnb, t}, c: [3]bool{ucb, tcb, false}})
	c.replaceNeighbor(unc, u, t)
	c.replaceNeighbor(tnb, t, u)
	return t, u
}

func (c *cdt) legalize(t, i int) {
	type item struct{ t, i int }
	stack := []item{{t, i}}
	for n := 0; len(stack) > 0 && n < 100000; n++ {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		tr := &c.tris[it.t]
		if tr.c[it.i] {
			continue
		}
		u := tr.n[it.i]
		if u < 0 {
			continue
		}
		ut := &c.tris[u]
		j := -1
		for k := range 3 {
			if ut.n[k] == it.t {
				j = k
			}
		}
		if j < 0 {
			continue
		}
		d := ut.v[j]
		if !c.inCircle(tr.v[0], tr.v[1], tr.v[2], d) {
			continue
		}
		t1, t2 := c.flip(it.t, it.i)
		stack = append(stack, item{t1, 0}, item{t2, 0})
	}
}

func edgeKey(a, b int) [2]int {
	if a > b {
		a, b = b, a
	}
	return [2]int{a, b}
}

// findEdge returns a triangle and index such that the edge opposite v[i]
// joins a and b (in either direction).
func (c *cdt) findEdge(a, b int) (int, int, bool) {
	start := c.vt[a]
	t := start
	// Walk around a in one direction, then the other if the fan is open.
	for dir := 0; dir < 2; dir++ {
		t = start
		for steps := 0; steps < 10000 && t >= 0; steps++ {
			tr := &c.tris[t]
			k := -1
			for m := range 3 {
				if tr.v[m] == a {
					k = m
				}
			}
			if k < 0 {
				break
			}
			if tr.v[(k+1)%3] == b {
				return t, (k + 2) % 3, true
			}
			if tr.v[(k+2)%3] == b {
				return t, (k + 1) % 3, true
			}
			var next int
			if dir == 0 {
				next = tr.n[(k+2)%3]
			} else {
				next = tr.n[(k+1)%3]
			}
			if next == start {
				return 0, 0, false
			}
			t = next
		}
	}
	return 0, 0, false
}

func (c *cdt) markConstrained(a, b int) {
	t, i, ok := c.findEdge(a, b)
	if !ok {
		return
	}
	c.tris[t].c[i] = true
	if u := c.tris[t].n[i]; u >= 0 {
		for k := range 3 {
			if c.tris[u].n[k] == t {
				c.tris[u].c[k] = true
			}
		}
	}
}

// addConstraint forces the edge a-b into the triangulation.
func (c *cdt) addConstraint(a, b int) error {
	return c.addConstraintDepth(a, b, 0)
}

func (c *cdt) addConstraintDepth(a, b, depth int) error {
	if a == b {
		return nil
	}
	if depth > 64 {
		return errCDT
	}
	if _, _, ok := c.findEdge(a, b); ok {
		c.markConstrained(a, b)
		c.ccount[edgeKey(a, b)]++
		return nil
	}
	// Find the triangle around a through which the segment leaves a.
	start := c.vt[a]
	t := start
	var p, q int
	found := false
	for steps := 0; steps < 10000; steps++ {
		tr := &c.tris[t]
		k := -1
		for m := range 3 {
			if tr.v[m] == a {
				k = m
			}
		}
		if k < 0 {
			return errCDT
		}
		pp, qq := tr.v[(k+1)%3], tr.v[(k+2)%3]
		op := c.orient(a, pp, b)
		oq := c.orient(a, qq, b)
		if math.Abs(op) <= cdtEps && c.between(a, b, pp) {
			if err := c.addConstraintDepth(a, pp, depth+1); err != nil {
				return err
			}
			return c.addConstraintDepth(pp, b, depth+1)
		}
		if math.Abs(oq) <= cdtEps && c.between(a, b, qq) {
			if err := c.addConstraintDepth(a, qq, depth+1); err != nil {
				return err
			}
			return c.addConstraintDepth(qq, b, depth+1)
		}
		if op > 0 && oq < 0 {
			p, q = pp, qq
			found = true
			break
		}
		t = tr.n[(k+2)%3]
		if t < 0 || t == start {
			break
		}
	}
	if !found {
		return errCDT
	}
	// Walk along the segment collecting crossed edges. p is right of a->b,
	// q is left.
	var crossed [][2]int
	crossed = append(crossed, [2]int{p, q})
	cur := t
	for steps := 0; steps < 100000; steps++ {
		// Triangle across edge p-q from cur.
		tr := &c.tris[cur]
		e := -1
		for m := range 3 {
			x, y := tr.v[(m+1)%3], tr.v[(m+2)%3]
			if (x == p && y == q) || (x == q && y == p) {
				e = m
			}
		}
		if e < 0 {
			return errCDT
		}
		u := tr.n[e]
		if u < 0 {
			return errCDT
		}
		ut := &c.tris[u]
		r := -1
		for m := range 3 {
			if ut.v[m] != p && ut.v[m] != q {
				r = ut.v[m]
			}
		}
		if r == b {
			break
		}
		o := c.orient(a, b, r)
		if math.Abs(o) <= cdtEps {
			if err := c.addConstraintDepth(a, r, depth+1); err != nil {
				return err
			}
			return c.addConstraintDepth(r, b, depth+1)
		}
		if o > 0 {
			q = r
		} else {
			p = r
		}
		crossed = append(crossed, [2]int{p, q})
		cur = u
	}
	// Remove crossed edges by flipping.
	var created [][2]int
	limit := 50*len(crossed)*len(crossed) + 1000
	for n := 0; len(crossed) > 0; n++ {
		if n > limit {
			return errCDT
		}
		e := crossed[0]
		crossed = crossed[1:]
		t, i, ok := c.findEdge(e[0], e[1])
		if !ok {
			continue
		}
		tr := c.tris[t]
		u := tr.n[i]
		if u < 0 {
			return errCDT
		}
		o1 := tr.v[i]
		var o2 int
		for m := range 3 {
			if c.tris[u].n[m] == t {
				o2 = c.tris[u].v[m]
			}
		}
		x, y := e[0], e[1]
		// Convex quad check: o1-o2 must properly cross x-y.
		if !(c.orient(o1, o2, x)*c.orient(o1, o2, y) < 0 && c.orient(x, y, o1)*c.orient(x, y, o2) < 0) {
			crossed = append(crossed, e)
			continue
		}
		c.flip(t, i)
		ne := [2]int{o1, o2}
		if ne[0] != a && ne[0] != b && ne[1] != a && ne[1] != b &&
			c.orient(a, b, ne[0])*c.orient(a, b, ne[1]) < 0 {
			crossed = append(crossed, ne)
		} else {
			created = append(created, ne)
		}
	}
	if _, _, ok := c.findEdge(a, b); !ok {
		return errCDT
	}
	c.markConstrained(a, b)
	c.ccount[edgeKey(a, b)]++
	// Restore the Delaunay property around newly created edges.
	for pass := 0; pass < 8; pass++ {
		changed := false
		for k, e := range created {
			if (e[0] == a && e[1] == b) || (e[0] == b && e[1] == a) {
				continue
			}
			t, i, ok := c.findEdge(e[0], e[1])
			if !ok || c.tris[t].c[i] {
				continue
			}
			u := c.tris[t].n[i]
			if u < 0 {
				continue
			}
			var d int
			for m := range 3 {
				if c.tris[u].n[m] == t {
					d = c.tris[u].v[m]
				}
			}
			tr := c.tris[t]
			if c.inCircle(tr.v[0], tr.v[1], tr.v[2], d) {
				// Only flip if the quad is convex.
				x, y := tr.v[(i+1)%3], tr.v[(i+2)%3]
				o1 := tr.v[i]
				if c.orient(o1, d, x)*c.orient(o1, d, y) < 0 && c.orient(x, y, o1)*c.orient(x, y, d) < 0 {
					c.flip(t, i)
					created[k] = [2]int{o1, d}
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}
	return nil
}

func (c *cdt) between(a, b, p int) bool {
	dx, dy := c.px[b]-c.px[a], c.py[b]-c.py[a]
	t := ((c.px[p]-c.px[a])*dx + (c.py[p]-c.py[a])*dy) / (dx*dx + dy*dy)
	return t > 0 && t < 1
}

// inside classifies triangles by the parity of constrained edges crossed
// from the outside, returning a flag per triangle.
func (c *cdt) inside() []bool {
	depth := make([]int, len(c.tris))
	for i := range depth {
		depth[i] = -1
	}
	// Seed with a triangle touching a super vertex.
	seed := -1
	for t := range c.tris {
		tr := &c.tris[t]
		if tr.v[0] < 3 || tr.v[1] < 3 || tr.v[2] < 3 {
			seed = t
			break
		}
	}
	out := make([]bool, len(c.tris))
	if seed < 0 {
		return out
	}
	frontier := []int{seed}
	depth[seed] = 0
	for level := 0; len(frontier) > 0; level++ {
		var next []int
		stack := frontier
		for len(stack) > 0 {
			t := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			tr := &c.tris[t]
			for i := range 3 {
				u := tr.n[i]
				if u < 0 || depth[u] >= 0 {
					continue
				}
				boundary := false
				if tr.c[i] {
					k := edgeKey(tr.v[(i+1)%3], tr.v[(i+2)%3])
					boundary = c.ccount[k]%2 == 1
				}
				if boundary {
					next = append(next, u)
				} else {
					depth[u] = level
					stack = append(stack, u)
				}
			}
		}
		frontier = frontier[:0]
		for _, u := range next {
			if depth[u] < 0 {
				depth[u] = level + 1
				frontier = append(frontier, u)
			}
		}
	}
	for t := range c.tris {
		tr := &c.tris[t]
		if tr.v[0] < 3 || tr.v[1] < 3 || tr.v[2] < 3 {
			continue
		}
		out[t] = depth[t]%2 == 1
	}
	return out
}

// point returns the user coordinates of vertex v.
func (c *cdt) point(v int) (float64, float64) {
	return c.px[v]/c.inv + c.ox, c.py[v]/c.inv + c.oy
}
