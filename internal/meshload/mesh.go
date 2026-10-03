// Package meshload reads triangle mesh formats (OBJ, STL and 3MF) into the
// viewer's model representation (the same one produced by the step package).
package meshload

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/AndreRenaud/stepview/internal/step"
)

// defaultColour matches the STEP loader's colour for unstyled solids.
var defaultColour = [3]float32{0.72, 0.73, 0.76}

// maxValence is the number of triangles around a vertex above which its
// corners are shaded flat, to keep smoothing from going quadratic.
const maxValence = 1024

// creaseCos is the cosine of the largest angle between adjacent triangles
// that is shaded smoothly when a file has no normals of its own.
var creaseCos = float32(math.Cos(35 * math.Pi / 180))

// LoadFile reads an OBJ, STL or 3MF file, chosen by its extension.
func LoadFile(path string) (*step.Model, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".obj":
		return LoadOBJ(path)
	case ".stl":
		return LoadSTL(path)
	case ".3mf":
		return Load3MF(path)
	}
	return nil, fmt.Errorf("meshload: unsupported file type %q", filepath.Ext(path))
}

// baseName is the file name without its directory or extension.
func baseName(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// builder collects triangles as a soup and turns them into an indexed mesh.
type builder struct {
	pos     [][3]float32 // three per triangle
	col     [][3]float32 // three per triangle
	nrm     [][3]float32 // three per triangle (zero when the file has none)
	hasNrm  []bool       // per triangle
	dropped int          // triangles with non-finite coordinates
}

func finite(p [3]float32) bool {
	for _, v := range p {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return false
		}
	}
	return true
}

// add appends a triangle with per-corner colours. n holds per-corner
// normals from the file, or is nil.
func (b *builder) add(p [3][3]float32, c [3][3]float32, n *[3][3]float32) {
	if !finite(p[0]) || !finite(p[1]) || !finite(p[2]) {
		b.dropped++
		return
	}
	b.pos = append(b.pos, p[0], p[1], p[2])
	b.col = append(b.col, c[0], c[1], c[2])
	if n != nil && finite(n[0]) && finite(n[1]) && finite(n[2]) {
		b.nrm = append(b.nrm, n[0], n[1], n[2])
		b.hasNrm = append(b.hasNrm, true)
	} else {
		b.nrm = append(b.nrm, [3]float32{}, [3]float32{}, [3]float32{})
		b.hasNrm = append(b.hasNrm, false)
	}
}

// addPolygon fan-triangulates a convex polygon.
func (b *builder) addPolygon(p [][3]float32, c [][3]float32, n [][3]float32) {
	for k := 2; k < len(p); k++ {
		tp := [3][3]float32{p[0], p[k-1], p[k]}
		tc := [3][3]float32{c[0], c[k-1], c[k]}
		if n != nil {
			b.add(tp, tc, &[3][3]float32{n[0], n[k-1], n[k]})
		} else {
			b.add(tp, tc, nil)
		}
	}
}

func (b *builder) triangles() int { return len(b.pos) / 3 }

func vec(p [3]float32) step.Vec3 {
	return step.Vec3{X: float64(p[0]), Y: float64(p[1]), Z: float64(p[2])}
}

// normalize returns v scaled to unit length; it works in float64 so that
// tiny triangles do not underflow.
func normalize(v [3]float32) ([3]float32, bool) {
	x, y, z := float64(v[0]), float64(v[1]), float64(v[2])
	l := math.Sqrt(x*x + y*y + z*z)
	if !(l > 0) || math.IsInf(l, 0) {
		return v, false
	}
	return [3]float32{float32(x / l), float32(y / l), float32(z / l)}, true
}

// mesh builds the indexed mesh, or returns nil if there are no usable
// triangles. Coincident vertices are welded; where the file gives no
// normals, each corner's normal averages the adjacent triangles that meet
// it at less than the crease angle, so hard edges stay sharp.
func (b *builder) mesh() *step.Mesh {
	nt := b.triangles()
	// Face normals, area weighted; degenerate triangles are dropped.
	fn := make([][3]float32, nt)
	keep := make([]bool, nt)
	for t := range nt {
		a, bb, c := vec(b.pos[t*3]), vec(b.pos[t*3+1]), vec(b.pos[t*3+2])
		n := bb.Sub(a).Cross(c.Sub(a))
		fn[t] = [3]float32{float32(n.X), float32(n.Y), float32(n.Z)}
		_, keep[t] = normalize(fn[t])
	}

	// Weld positions.
	ids := make(map[[3]float32]uint32, len(b.pos)/2)
	pid := make([]uint32, len(b.pos))
	var upos [][3]float32
	for i, p := range b.pos {
		if !keep[i/3] {
			continue
		}
		id, ok := ids[p]
		if !ok {
			id = uint32(len(upos))
			ids[p] = id
			upos = append(upos, p)
		}
		pid[i] = id
	}
	if len(upos) == 0 {
		return nil
	}
	if b.insideOut(upos, pid, keep) {
		for t := range nt {
			i, j := t*3+1, t*3+2
			b.pos[i], b.pos[j] = b.pos[j], b.pos[i]
			b.col[i], b.col[j] = b.col[j], b.col[i]
			b.nrm[i], b.nrm[j] = b.nrm[j], b.nrm[i]
			pid[i], pid[j] = pid[j], pid[i]
			for k := t * 3; k < t*3+3; k++ {
				b.nrm[k] = [3]float32{-b.nrm[k][0], -b.nrm[k][1], -b.nrm[k][2]}
			}
			fn[t] = [3]float32{-fn[t][0], -fn[t][1], -fn[t][2]}
		}
	}

	// Triangles around each welded position (compressed rows).
	start := make([]uint32, len(upos)+1)
	for i := range b.pos {
		if keep[i/3] {
			start[pid[i]+1]++
		}
	}
	for i := range upos {
		start[i+1] += start[i]
	}
	fill := append([]uint32(nil), start[:len(upos)]...)
	around := make([]uint32, start[len(upos)])
	for i := range b.pos {
		if keep[i/3] {
			around[fill[pid[i]]] = uint32(i / 3)
			fill[pid[i]]++
		}
	}

	unit := make([][3]float32, nt)
	for t := range nt {
		unit[t], _ = normalize(fn[t])
	}

	type vkey struct {
		p    uint32
		n, c [3]float32
	}
	verts := make(map[vkey]uint32, len(upos))
	m := &step.Mesh{Bounds: step.EmptyBox(), FaceStarts: []uint32{0}}
	for t := range nt {
		if !keep[t] {
			continue
		}
		for k := range 3 {
			i := t*3 + k
			var n [3]float32
			ok := false
			if b.hasNrm[t] {
				n, ok = normalize(b.nrm[i])
			}
			if ring := around[start[pid[i]]:start[pid[i]+1]]; !ok && len(ring) <= maxValence {
				var s [3]float32
				for _, o := range ring {
					u := unit[o]
					if u[0]*unit[t][0]+u[1]*unit[t][1]+u[2]*unit[t][2] >= creaseCos {
						s[0] += fn[o][0]
						s[1] += fn[o][1]
						s[2] += fn[o][2]
					}
				}
				n, ok = normalize(s)
			}
			if !ok {
				n = unit[t]
			}
			key := vkey{pid[i], n, b.col[i]}
			v, seen := verts[key]
			if !seen {
				v = uint32(len(m.Positions) / 3)
				verts[key] = v
				p := upos[pid[i]]
				m.Positions = append(m.Positions, p[0], p[1], p[2])
				m.Normals = append(m.Normals, n[0], n[1], n[2])
				m.Colors = append(m.Colors, b.col[i][0], b.col[i][1], b.col[i][2])
				m.Bounds.Extend(vec(p))
			}
			m.Indices = append(m.Indices, v)
		}
	}
	return m
}

// insideOut reports whether the triangles form a (nearly) closed surface
// that encloses a negative volume, as files exported with mirrored geometry
// often are. Open surfaces have no inside, so they are left alone; a few
// unmatched edges, from T-junctions or small holes, are tolerated.
func (b *builder) insideOut(upos [][3]float32, pid []uint32, keep []bool) bool {
	box := step.EmptyBox()
	for _, p := range upos {
		box.Extend(vec(p))
	}
	o := box.Center()
	edges := make(map[uint64]int32, len(pid))
	vol := 0.0
	for t, ok := range keep {
		if !ok {
			continue
		}
		for k := range 3 {
			a, c := pid[t*3+k], pid[t*3+(k+1)%3]
			edges[uint64(min(a, c))<<32|uint64(max(a, c))]++
		}
		p0, p1, p2 := vec(upos[pid[t*3]]).Sub(o), vec(upos[pid[t*3+1]]).Sub(o), vec(upos[pid[t*3+2]]).Sub(o)
		vol += p0.Dot(p1.Cross(p2))
	}
	shared := 0
	for _, n := range edges {
		if n == 2 {
			shared++
		}
	}
	return shared*10 >= len(edges)*9 && vol < 0
}

// finish wraps the root nodes in a model and fills in its statistics.
func finish(name string, roots []*step.Node, warnings []string) *step.Model {
	m := &step.Model{Name: name, Roots: roots, Warnings: warnings}
	var count func(n *step.Node)
	count = func(n *step.Node) {
		if n.Mesh != nil {
			m.Stats.Triangles += n.Mesh.TriangleCount()
			m.Stats.Solids++
		}
		for _, c := range n.Children {
			count(c)
		}
	}
	for _, r := range roots {
		count(r)
	}
	return m
}
