package main

import (
	"math"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/solarlune/tetra3d"

	"github.com/AndreRenaud/stepview/internal/step"
)

// creaseCos is the cosine of the dihedral angle above which an edge inside
// a face is drawn in wireframe mode (only relevant for meshes without face
// structure, such as glTF).
var creaseCos = math.Cos(35 * math.Pi / 180)

// featureEdges returns the model edges of a mesh as pairs of vertex
// indices: boundaries between B-rep faces, open boundaries and sharp
// creases. Smooth interior triangle edges are omitted.
func featureEdges(m *step.Mesh) []uint32 {
	type key [6]float32
	type info struct {
		a, b    uint32
		face    int
		n       step.Vec3
		count   int
		feature bool
	}
	pos := func(i uint32) [3]float32 {
		return [3]float32{m.Positions[i*3], m.Positions[i*3+1], m.Positions[i*3+2]}
	}
	edges := map[key]*info{}
	var order []*info
	faceEnd := func(f int) int {
		if f+1 < len(m.FaceStarts) {
			return int(m.FaceStarts[f+1])
		}
		return len(m.Indices)
	}
	nFaces := max(1, len(m.FaceStarts))
	for f := 0; f < nFaces; f++ {
		start := 0
		if f < len(m.FaceStarts) {
			start = int(m.FaceStarts[f])
		}
		for t := start; t+2 < faceEnd(f); t += 3 {
			idx := [3]uint32{m.Indices[t], m.Indices[t+1], m.Indices[t+2]}
			var p [3]step.Vec3
			for k, i := range idx {
				q := pos(i)
				p[k] = step.Vec3{X: float64(q[0]), Y: float64(q[1]), Z: float64(q[2])}
			}
			n := p[1].Sub(p[0]).Cross(p[2].Sub(p[0])).Norm()
			for k := 0; k < 3; k++ {
				a, b := idx[k], idx[(k+1)%3]
				pa, pb := pos(a), pos(b)
				if pa[0] > pb[0] || pa[0] == pb[0] && (pa[1] > pb[1] || pa[1] == pb[1] && pa[2] > pb[2]) {
					pa, pb = pb, pa
				}
				ek := key{pa[0], pa[1], pa[2], pb[0], pb[1], pb[2]}
				e, ok := edges[ek]
				if !ok {
					e = &info{a: a, b: b, face: f, n: n, count: 1}
					edges[ek] = e
					order = append(order, e)
					continue
				}
				e.count++
				if e.face != f || e.n.Dot(n) < creaseCos {
					e.feature = true
				}
			}
		}
	}
	var out []uint32
	for _, e := range order {
		if e.feature || e.count == 1 {
			out = append(out, e.a, e.b)
		}
	}
	return out
}

// edges returns (and caches) the feature edges of a mesh.
func (d *document) edges(m *step.Mesh) []uint32 {
	if d.edgeCache == nil {
		d.edgeCache = map[*step.Mesh][]uint32{}
	}
	e, ok := d.edgeCache[m]
	if !ok {
		e = featureEdges(m)
		d.edgeCache[m] = e
	}
	return e
}

// renderWireframe draws the model edges of all visible instances into dst,
// using the camera's projection.
func (v *view3D) renderWireframe(dst *ebiten.Image, lineWidth float32, dark bool) {
	d := v.doc
	w, h := v.camera.Size()
	fw, fh := float32(w), float32(h)
	vp := v.camera.ViewMatrix().Mult(v.camera.Projection())
	line := [3]float32{0.12, 0.13, 0.16}
	if dark {
		line = [3]float32{0.85, 0.87, 0.9}
	}
	half := lineWidth / 2
	var verts []ebiten.Vertex
	var idx []uint32
	flush := func() {
		if len(idx) > 0 {
			dst.DrawTriangles32(verts, idx, v.white, &ebiten.DrawTrianglesOptions{AntiAlias: true})
		}
		verts = verts[:0]
		idx = idx[:0]
	}
	for _, inst := range d.insts {
		if d.effectiveHidden(inst.node) {
			continue
		}
		col := line
		if d.isSelected(inst.node) {
			col = highlightColour
		}
		// Combine the instance transform, the Z-up to Y-up conversion and the
		// view-projection into one matrix (row-vector convention).
		wr := inst.world
		var mw tetra3d.Matrix4
		for i := 0; i < 3; i++ {
			c := toT(step.Vec3{X: wr.R[0][i], Y: wr.R[1][i], Z: wr.R[2][i]})
			mw[i] = [4]float32{c.X, c.Y, c.Z, 0}
		}
		t := toT(wr.T)
		mw[3] = [4]float32{t.X, t.Y, t.Z, 1}
		mvp := mw.Mult(vp)
		m := inst.mesh
		project := func(i uint32) (float32, float32, bool) {
			p := mvp.MultVecW(tetra3d.Vector3{X: m.Positions[i*3], Y: m.Positions[i*3+1], Z: m.Positions[i*3+2]})
			if p.W <= 1e-6 {
				return 0, 0, false
			}
			return p.X/p.W*fw + fw/2, p.Y/-p.W*fh + fh/2, true
		}
		e := d.edges(m)
		for k := 0; k+1 < len(e); k += 2 {
			x0, y0, ok0 := project(e[k])
			x1, y1, ok1 := project(e[k+1])
			if !ok0 || !ok1 {
				continue
			}
			if (x0 < 0 && x1 < 0) || (y0 < 0 && y1 < 0) || (x0 > fw && x1 > fw) || (y0 > fh && y1 > fh) {
				continue
			}
			dx, dy := x1-x0, y1-y0
			l := float32(math.Hypot(float64(dx), float64(dy)))
			if l < 1e-3 {
				continue
			}
			nx, ny := -dy/l*half, dx/l*half
			base := uint32(len(verts))
			for _, q := range [4][2]float32{{x0 + nx, y0 + ny}, {x0 - nx, y0 - ny}, {x1 + nx, y1 + ny}, {x1 - nx, y1 - ny}} {
				verts = append(verts, ebiten.Vertex{DstX: q[0], DstY: q[1], SrcX: 1, SrcY: 1, ColorR: col[0], ColorG: col[1], ColorB: col[2], ColorA: 1})
			}
			idx = append(idx, base, base+1, base+2, base+1, base+3, base+2)
			if len(verts) > 1<<20 {
				flush()
			}
		}
	}
	flush()
}
