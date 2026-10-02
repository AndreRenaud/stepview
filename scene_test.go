package main

import (
	"path/filepath"
	"testing"

	"github.com/AndreRenaud/stepview/internal/step"
)

func loadDoc(t *testing.T, name string) *document {
	t.Helper()
	path := filepath.Join("testdata", name)
	m, err := step.LoadFile(path, step.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return newDocument(path, m)
}

func visibleTriangles(d *document) int {
	return d.visibleTris
}

func TestDocumentGeometry(t *testing.T) {
	d := loadDoc(t, "as1_pe.stp")
	if got := visibleTriangles(d); got != d.triangles {
		t.Fatalf("%d triangles to draw, want %d", got, d.triangles)
	}
	last := d.insts[len(d.insts)-1]
	if n := last.vert0 + len(last.mesh.Positions)/3; n != len(d.verts) {
		t.Fatalf("instances cover %d vertices, have %d", n, len(d.verts))
	}
}

func TestDocumentHideAndSelect(t *testing.T) {
	d := loadDoc(t, "as1_pe.stp")
	plate := -1
	for i, n := range d.nodes {
		if n.name == "PLATE" {
			plate = i
		}
	}
	if plate < 0 {
		t.Fatal("PLATE not found")
	}
	plateTris := 0
	for _, ii := range d.nodes[plate].insts {
		plateTris += len(d.insts[ii].mesh.Indices) / 3
	}
	d.setHidden(plate, true)
	if got := visibleTriangles(d); got != d.triangles-plateTris {
		t.Errorf("after hiding: %d triangles, want %d", got, d.triangles-plateTris)
	}
	// Hidden geometry cannot be picked.
	c := d.insts[d.nodes[plate].insts[0]].bounds.Center()
	if hit := d.pick(c.Add(step.Vec3{Z: 1000}), step.Vec3{Z: -1}); hit == plate {
		t.Error("picked a hidden node")
	}
	d.setHidden(plate, false)
	if got := visibleTriangles(d); got != d.triangles {
		t.Errorf("after showing: %d triangles, want %d", got, d.triangles)
	}
	before := d.verts[d.insts[d.nodes[plate].insts[0]].vert0]
	d.setSelected(0)
	if !d.isSelected(plate) {
		t.Error("selecting the root should select its descendants")
	}
	if d.verts[d.insts[d.nodes[plate].insts[0]].vert0] == before {
		t.Error("selection did not change the colour")
	}
}

func TestPick(t *testing.T) {
	d := loadDoc(t, "block.stp")
	b := d.bounds
	c := b.Center()
	origin := step.Vec3{X: c.X, Y: c.Y, Z: b.Max.Z + 10}
	if hit := d.pick(origin, step.Vec3{Z: -1}); hit < 0 {
		t.Error("ray through the centre missed")
	}
	if hit := d.pick(origin.Add(step.Vec3{X: b.Diag() * 2}), step.Vec3{Z: -1}); hit >= 0 {
		t.Errorf("ray beside the model hit node %d", hit)
	}
}

func TestFeatureEdges(t *testing.T) {
	d := loadDoc(t, "block.stp")
	n := 0
	for _, inst := range d.insts {
		n += len(d.edges(inst.mesh)) / 2
	}
	// block.stp is a single box: 12 edges, each shared by two faces.
	if n != 12 {
		t.Errorf("block has %d feature edges, want 12", n)
	}
}

func TestSubtreeBoundsIgnoresVisibility(t *testing.T) {
	d := loadDoc(t, "as1_pe.stp")
	b := d.subtreeBounds(1)
	d.setHidden(1, true)
	if got := d.subtreeBounds(1); got != b {
		t.Errorf("bounds changed when hidden: %v vs %v", got, b)
	}
}
