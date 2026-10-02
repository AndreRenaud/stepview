package main

import (
	"math"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/AndreRenaud/stepview/internal/step"
)

// highlightColour is blended into selected geometry.
var highlightColour = [3]float32{1.0, 0.55, 0.1}

// studioLights is a fixed lighting rig in model coordinates (Z up): a key
// light from the upper front right, a fill from the left rear and a weak
// light from below.
var studioLights = []struct {
	dir    step.Vec3
	energy float32
}{
	{step.Vec3{X: 0.5, Y: -0.6, Z: 0.75}.Norm(), 0.65},
	{step.Vec3{X: -0.7, Y: 0.5, Z: 0.3}.Norm(), 0.35},
	{step.Vec3{X: 0.1, Y: 0.3, Z: -1}.Norm(), 0.15},
}

const ambientLight = 0.3

// lighting returns the brightness for a surface normal.
func lighting(n step.Vec3) float32 {
	l := float32(ambientLight)
	for _, s := range studioLights {
		if d := float32(n.Dot(s.dir)); d > 0 {
			l += d * s.energy
		}
	}
	return l
}

// viewNode is a node of the flattened product tree, in depth-first order.
type viewNode struct {
	name      string
	depth     int
	parent    int
	end       int // index one past the last descendant
	hidden    bool
	collapsed bool
	insts     []int
}

// instance is one placed occurrence of a mesh.
type instance struct {
	node   int
	mesh   *step.Mesh
	world  step.Affine // model space (mm, Z up)
	bounds step.Box    // world bounding box
	vert0  int         // first vertex in document.verts
}

// document is a loaded model prepared for display.
type document struct {
	path  string
	model *step.Model
	nodes []viewNode
	insts []instance

	// The vertices of every instance, in world space, ready for the
	// renderer.
	pos         []float32       // 3 per vertex
	shade       []float32       // 4 per vertex: unlit colour and brightness
	verts       []ebiten.Vertex // colours; the renderer fills in the rest
	visible     []int           // instances that are not hidden
	visibleTris int

	selected  int // node index, or -1
	bounds    step.Box
	triangles int
	gen       int // incremented on every visual change

	edgeCache map[*step.Mesh][]uint32 // feature edges for wireframe mode
}

func newDocument(path string, m *step.Model) *document {
	d := &document{path: path, model: m, selected: -1, bounds: step.EmptyBox()}
	for _, r := range m.Roots {
		d.addNode(r, step.Identity(), -1, 0)
	}
	// Collapse everything below the top level of large trees.
	for i := range d.nodes {
		n := &d.nodes[i]
		n.collapsed = n.end > i+1 && n.depth >= 1
	}
	d.buildGeometry()
	return d
}

func (d *document) addNode(n *step.Node, parentWorld step.Affine, parent, depth int) {
	world := parentWorld.Mul(n.Local)
	// Collapse wrapper chains such as KiCad's "C_0402 > C_0402".
	for n.Mesh == nil && len(n.Children) == 1 && n.Children[0].Name == n.Name {
		n = n.Children[0]
		world = world.Mul(n.Local)
	}
	idx := len(d.nodes)
	d.nodes = append(d.nodes, viewNode{name: n.Name, depth: depth, parent: parent})
	if n.Mesh != nil && len(n.Mesh.Indices) > 0 {
		b := n.Mesh.Bounds.Transform(world)
		d.insts = append(d.insts, instance{node: idx, mesh: n.Mesh, world: world, bounds: b})
		d.nodes[idx].insts = append(d.nodes[idx].insts, len(d.insts)-1)
		d.bounds.Union(b)
		d.triangles += len(n.Mesh.Indices) / 3
	}
	for _, c := range n.Children {
		d.addNode(c, world, idx, depth+1)
	}
	d.nodes[idx].end = len(d.nodes)
}

// buildGeometry places every instance's vertices in world space, with
// lighting baked into their colours.
func (d *document) buildGeometry() {
	nv := 0
	for i := range d.insts {
		d.insts[i].vert0 = nv
		nv += len(d.insts[i].mesh.Positions) / 3
	}
	d.pos = make([]float32, nv*3)
	d.shade = make([]float32, nv*4)
	d.verts = make([]ebiten.Vertex, nv)
	parallelEach(len(d.insts), func(i int) {
		in := &d.insts[i]
		m := in.mesh
		for k := 0; k < len(m.Positions)/3; k++ {
			p := in.world.Apply(step.Vec3{X: float64(m.Positions[k*3]), Y: float64(m.Positions[k*3+1]), Z: float64(m.Positions[k*3+2])})
			n := in.world.ApplyNormal(step.Vec3{X: float64(m.Normals[k*3]), Y: float64(m.Normals[k*3+1]), Z: float64(m.Normals[k*3+2])})
			v := in.vert0 + k
			d.pos[v*3], d.pos[v*3+1], d.pos[v*3+2] = float32(p.X), float32(p.Y), float32(p.Z)
			copy(d.shade[v*4:v*4+3], m.Colors[k*3:k*3+3])
			d.shade[v*4+3] = lighting(n)
		}
		d.paint(i)
	})
	d.updateVisible()
}

// paint sets the colours of an instance's vertices.
func (d *document) paint(i int) {
	in := &d.insts[i]
	sel := d.isSelected(in.node)
	for v := in.vert0; v < in.vert0+len(in.mesh.Positions)/3; v++ {
		col := [3]float32(d.shade[v*4 : v*4+3])
		if sel {
			for j := range col {
				col[j] = col[j]*0.35 + highlightColour[j]*0.65
			}
		}
		l := d.shade[v*4+3]
		d.verts[v].ColorR = min(1, col[0]*l)
		d.verts[v].ColorG = min(1, col[1]*l)
		d.verts[v].ColorB = min(1, col[2]*l)
		d.verts[v].ColorA = 1
	}
}

// updateVisible lists the instances that are not hidden.
func (d *document) updateVisible() {
	d.visible = d.visible[:0]
	d.visibleTris = 0
	for i, in := range d.insts {
		if !d.effectiveHidden(in.node) {
			d.visible = append(d.visible, i)
			d.visibleTris += len(in.mesh.Indices) / 3
		}
	}
}

// effectiveHidden reports whether a node or any ancestor is hidden.
func (d *document) effectiveHidden(i int) bool {
	for i >= 0 {
		if d.nodes[i].hidden {
			return true
		}
		i = d.nodes[i].parent
	}
	return false
}

// isSelected reports whether node i is within the selected subtree.
func (d *document) isSelected(i int) bool {
	s := d.selected
	return s >= 0 && i >= s && i < d.nodes[s].end
}

// setHidden changes a node's visibility.
func (d *document) setHidden(i int, hidden bool) {
	if i < 0 || i >= len(d.nodes) || d.nodes[i].hidden == hidden {
		return
	}
	d.nodes[i].hidden = hidden
	d.updateVisible()
	d.gen++
}

// setSelected changes the selected node (-1 for none).
func (d *document) setSelected(i int) {
	if i == d.selected {
		return
	}
	old := d.selected
	d.selected = i
	for _, sub := range []int{old, i} {
		if sub < 0 || sub >= len(d.nodes) {
			continue
		}
		for n := sub; n < d.nodes[sub].end; n++ {
			for _, ii := range d.nodes[n].insts {
				d.paint(ii)
			}
		}
	}
	d.gen++
}

// subtreeBounds returns the bounds of a subtree's visible geometry, or of
// all its geometry when none of it is visible.
func (d *document) subtreeBounds(sub int) step.Box {
	b := d.visibleBounds(sub)
	if !b.Empty() || sub < 0 || sub >= len(d.nodes) {
		return b
	}
	for _, inst := range d.insts {
		if inst.node >= sub && inst.node < d.nodes[sub].end {
			b.Union(inst.bounds)
		}
	}
	return b
}

// visibleBounds returns the bounds of visible geometry, optionally limited
// to a subtree.
func (d *document) visibleBounds(sub int) step.Box {
	b := step.EmptyBox()
	lo, hi := 0, len(d.nodes)
	if sub >= 0 && sub < len(d.nodes) {
		lo, hi = sub, d.nodes[sub].end
	}
	for _, inst := range d.insts {
		if inst.node < lo || inst.node >= hi || d.effectiveHidden(inst.node) {
			continue
		}
		b.Union(inst.bounds)
	}
	return b
}

// pick returns the node hit by a ray in model coordinates, or -1.
func (d *document) pick(origin, dir step.Vec3) int {
	best := math.Inf(1)
	hit := -1
	for _, inst := range d.insts {
		if d.effectiveHidden(inst.node) {
			continue
		}
		if !rayBox(origin, dir, inst.bounds, best) {
			continue
		}
		inv := inst.world.Inverse()
		o := inv.Apply(origin)
		dr := inv.ApplyDir(dir)
		// An affine map preserves the ray parameter, so local hit distances
		// compare directly with world ones.
		m := inst.mesh
		if !rayBox(o, dr, m.Bounds, best) {
			continue
		}
		for t := 0; t+2 < len(m.Indices); t += 3 {
			a := vtx(m, m.Indices[t])
			b := vtx(m, m.Indices[t+1])
			c := vtx(m, m.Indices[t+2])
			if tt, ok := rayTriangle(o, dr, a, b, c); ok && tt < best {
				best = tt
				hit = inst.node
			}
		}
	}
	return hit
}

func vtx(m *step.Mesh, i uint32) step.Vec3 {
	return step.Vec3{X: float64(m.Positions[i*3]), Y: float64(m.Positions[i*3+1]), Z: float64(m.Positions[i*3+2])}
}

// rayTriangle is the Möller–Trumbore intersection test, returning the ray
// parameter of the hit.
func rayTriangle(o, d, a, b, c step.Vec3) (float64, bool) {
	e1 := b.Sub(a)
	e2 := c.Sub(a)
	p := d.Cross(e2)
	det := e1.Dot(p)
	if math.Abs(det) < 1e-18 {
		return 0, false
	}
	inv := 1 / det
	s := o.Sub(a)
	u := s.Dot(p) * inv
	if u < 0 || u > 1 {
		return 0, false
	}
	q := s.Cross(e1)
	v := d.Dot(q) * inv
	if v < 0 || u+v > 1 {
		return 0, false
	}
	t := e2.Dot(q) * inv
	return t, t > 0
}

// rayBox reports whether the ray hits the box before maxT.
func rayBox(o, d step.Vec3, b step.Box, maxT float64) bool {
	if b.Empty() {
		return false
	}
	t0, t1 := 0.0, maxT
	for axis := range 3 {
		var oo, dd, lo, hi float64
		switch axis {
		case 0:
			oo, dd, lo, hi = o.X, d.X, b.Min.X, b.Max.X
		case 1:
			oo, dd, lo, hi = o.Y, d.Y, b.Min.Y, b.Max.Y
		default:
			oo, dd, lo, hi = o.Z, d.Z, b.Min.Z, b.Max.Z
		}
		if math.Abs(dd) < 1e-300 {
			if oo < lo || oo > hi {
				return false
			}
			continue
		}
		ta, tb := (lo-oo)/dd, (hi-oo)/dd
		if ta > tb {
			ta, tb = tb, ta
		}
		t0 = math.Max(t0, ta)
		t1 = math.Min(t1, tb)
		if t0 > t1 {
			return false
		}
	}
	return true
}
