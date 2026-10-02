package main

import (
	"math"

	"github.com/solarlune/tetra3d"

	"github.com/AndreRenaud/stepview/internal/step"
)

// maxChunkTris is the triangle budget of one rendered chunk (tetra3d's
// limit for one mesh part is 21845).
const maxChunkTris = 12000

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
}

type chunkEntry struct {
	inst       int
	tri0, tri1 int // triangle range within the instance mesh
}

type chunk struct {
	entries []chunkEntry
	model   *tetra3d.Model
}

// document is a loaded model prepared for display.
type document struct {
	path      string
	model     *step.Model
	nodes     []viewNode
	insts     []instance
	chunks    []*chunk
	instChunk [][]int // instance -> chunk indices

	scene     *tetra3d.Scene
	container *tetra3d.Node
	material  *tetra3d.Material

	selected  int // node index, or -1
	bounds    step.Box
	triangles int
	gen       int // incremented on every visual change

	edgeCache map[*step.Mesh][]uint32 // feature edges for wireframe mode
}

// toT converts model coordinates (Z up) to tetra3d coordinates (Y up).
func toT(p step.Vec3) tetra3d.Vector3 {
	return tetra3d.Vector3{X: float32(p.X), Y: float32(p.Z), Z: float32(-p.Y)}
}

// fromT converts tetra3d coordinates back to model coordinates.
func fromT(v tetra3d.Vector3) step.Vec3 {
	return step.Vec3{X: float64(v.X), Y: float64(-v.Z), Z: float64(v.Y)}
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
	d.scene = tetra3d.NewScene("model")
	d.scene.World.FogOn = false
	d.scene.World.LightingOn = false
	d.container = tetra3d.NewNode("geometry")
	d.scene.Root.AddChildren(d.container)
	d.material = tetra3d.NewMaterial("vertex colours")
	d.material.BackfaceCulling = true
	// Lighting is baked into the vertex colours (see lighting), which is
	// much cheaper than tetra3d's per-frame vertex lighting.
	d.material.Shadeless = true
	d.buildChunks()
	for _, c := range d.chunks {
		d.rebuildChunk(c)
	}
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

// buildChunks distributes instance geometry over chunks.
//
// tetra3d depth tests between mesh parts but only sorts triangles (by
// centre) within one, and every part costs several full-screen passes. So
// instances share a chunk only when their bounding boxes are disjoint, where
// painter's ordering is reliable; touching or nested parts (components on a
// board, bolts in holes) end up in different chunks and are depth tested.
func (d *document) buildChunks() {
	type piece struct {
		inst, tri0, tri1 int
	}
	type openChunk struct {
		c     *chunk
		idx   int
		tris  int
		boxes []step.Box
	}
	var pieces []piece
	for ii, inst := range d.insts {
		m := inst.mesh
		total := len(m.Indices) / 3
		if total <= maxChunkTris {
			pieces = append(pieces, piece{ii, 0, total})
			continue
		}
		// Split large meshes at face boundaries.
		bounds := make([]int, 0, len(m.FaceStarts)+1)
		for _, s := range m.FaceStarts {
			bounds = append(bounds, int(s)/3)
		}
		bounds = append(bounds, total)
		t, bi := 0, 0
		for t < total {
			end := min(total, t+maxChunkTris)
			if end < total {
				for bi < len(bounds) && bounds[bi] <= t {
					bi++
				}
				best := -1
				for k := bi; k < len(bounds) && bounds[k] <= end; k++ {
					best = bounds[k]
				}
				if best > t {
					end = best
				}
			}
			pieces = append(pieces, piece{ii, t, end})
			t = end
		}
	}
	d.instChunk = make([][]int, len(d.insts))
	var open []*openChunk
	for _, p := range pieces {
		box := d.insts[p.inst].bounds
		n := p.tri1 - p.tri0
		var target *openChunk
		for _, oc := range open {
			if oc.tris+n > maxChunkTris {
				continue
			}
			ok := true
			for _, b := range oc.boxes {
				if boxesIntersect(b, box) {
					ok = false
					break
				}
			}
			if ok {
				target = oc
				break
			}
		}
		if target == nil {
			target = &openChunk{c: &chunk{}, idx: len(d.chunks)}
			d.chunks = append(d.chunks, target.c)
			open = append(open, target)
		}
		target.c.entries = append(target.c.entries, chunkEntry{inst: p.inst, tri0: p.tri0, tri1: p.tri1})
		target.tris += n
		target.boxes = append(target.boxes, box)
		if l := d.instChunk[p.inst]; len(l) == 0 || l[len(l)-1] != target.idx {
			d.instChunk[p.inst] = append(d.instChunk[p.inst], target.idx)
		}
		// Retire full chunks so the search stays short.
		if target.tris > maxChunkTris*9/10 {
			for i, oc := range open {
				if oc == target {
					open = append(open[:i], open[i+1:]...)
					break
				}
			}
		}
	}
}

func boxesIntersect(a, b step.Box) bool {
	return a.Min.X <= b.Max.X && b.Min.X <= a.Max.X &&
		a.Min.Y <= b.Max.Y && b.Min.Y <= a.Max.Y &&
		a.Min.Z <= b.Max.Z && b.Min.Z <= a.Max.Z
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

func (d *document) rebuildChunk(c *chunk) {
	if c.model != nil {
		d.container.RemoveChildren(c.model)
		c.model = nil
	}
	var verts []tetra3d.VertexInfo
	var idx []int
	remap := map[uint32]int{}
	for _, e := range c.entries {
		inst := &d.insts[e.inst]
		if d.effectiveHidden(inst.node) {
			continue
		}
		sel := d.isSelected(inst.node)
		m := inst.mesh
		clear(remap)
		for t := e.tri0; t < e.tri1; t++ {
			for k := 0; k < 3; k++ {
				vi := m.Indices[t*3+k]
				if j, ok := remap[vi]; ok {
					idx = append(idx, j)
					continue
				}
				p := inst.world.Apply(step.Vec3{X: float64(m.Positions[vi*3]), Y: float64(m.Positions[vi*3+1]), Z: float64(m.Positions[vi*3+2])})
				n := inst.world.ApplyNormal(step.Vec3{X: float64(m.Normals[vi*3]), Y: float64(m.Normals[vi*3+1]), Z: float64(m.Normals[vi*3+2])})
				col := [3]float32{m.Colors[vi*3], m.Colors[vi*3+1], m.Colors[vi*3+2]}
				if sel {
					for j := range col {
						col[j] = col[j]*0.35 + highlightColour[j]*0.65
					}
				}
				l := lighting(n)
				for j := range col {
					col[j] = min(1, col[j]*l)
				}
				tp, tn := toT(p), toT(n)
				verts = append(verts, tetra3d.VertexInfo{
					X: tp.X, Y: tp.Y, Z: tp.Z,
					NormalX: tn.X, NormalY: tn.Y, NormalZ: tn.Z,
					Colors: []tetra3d.Color4{{R: col[0], G: col[1], B: col[2], A: 1}},
				})
				j := len(verts) - 1
				remap[vi] = j
				idx = append(idx, j)
			}
		}
	}
	if len(idx) == 0 {
		return
	}
	mesh := tetra3d.NewMesh("chunk")
	part := mesh.AddMeshPart(d.material)
	mesh.AddVertices(verts...)
	part.AddTriangles(idx...)
	mesh.VertexActiveColorChannel = 0
	mesh.UpdateBounds()
	c.model = tetra3d.NewModel("chunk", mesh)
	d.container.AddChildren(c.model)
}

// rebuildSubtree rebuilds every chunk containing geometry of node i's
// subtree.
func (d *document) rebuildSubtree(i int) {
	if i < 0 || i >= len(d.nodes) {
		return
	}
	dirty := map[int]bool{}
	for n := i; n < d.nodes[i].end; n++ {
		for _, ii := range d.nodes[n].insts {
			for _, c := range d.instChunk[ii] {
				dirty[c] = true
			}
		}
	}
	for c := range dirty {
		d.rebuildChunk(d.chunks[c])
	}
	d.gen++
}

// setHidden changes a node's visibility.
func (d *document) setHidden(i int, hidden bool) {
	if i < 0 || i >= len(d.nodes) || d.nodes[i].hidden == hidden {
		return
	}
	d.nodes[i].hidden = hidden
	d.rebuildSubtree(i)
}

// setSelected changes the selected node (-1 for none).
func (d *document) setSelected(i int) {
	if i == d.selected {
		return
	}
	old := d.selected
	d.selected = i
	d.rebuildSubtree(old)
	d.rebuildSubtree(i)
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
	for axis := 0; axis < 3; axis++ {
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
