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

func TestInfoText(t *testing.T) {
	b := step.Box{Min: step.Vec3{X: -10, Y: 0, Z: 0}, Max: step.Vec3{X: 10, Y: 25.4, Z: 5}}
	labels, values := infoText(b, unitInches)
	if labels != "Position\nSize" {
		t.Errorf("labels = %q", labels)
	}
	if want := "0.000, 0.500, 0.098 in\n0.787 × 1.000 × 0.197 in"; values != want {
		t.Errorf("values = %q, want %q", values, want)
	}
	if got := unitMillimetres.format(-0.001); got != "0.00" {
		t.Errorf("format(-0.001) = %q, want 0.00", got)
	}
	if got := unitMetres.format(-1234); got != "-1.2340" {
		t.Errorf("format(-1234) = %q, want -1.2340", got)
	}
}

func TestPickThrough(t *testing.T) {
	d := loadDoc(t, "as1_pe.stp")
	// Look for a ray along Y that passes through several parts.
	b := d.bounds
	var hits []int
	var origin step.Vec3
	for i := 1; i < 20 && len(hits) < 2; i++ {
		for j := 1; j < 20 && len(hits) < 2; j++ {
			origin = step.Vec3{
				X: b.Min.X + (b.Max.X-b.Min.X)*float64(i)/20,
				Y: b.Min.Y - 10,
				Z: b.Min.Z + (b.Max.Z-b.Min.Z)*float64(j)/20,
			}
			hits = d.pickAll(origin, step.Vec3{Y: 1})
		}
	}
	if len(hits) < 2 {
		t.Fatal("no ray through two parts")
	}
	if got := d.pick(origin, step.Vec3{Y: 1}); got != hits[0] {
		t.Errorf("pick = %d, want the nearest hit %d", got, hits[0])
	}
	// Clicking again steps back through the hits and wraps to the front.
	d.setSelected(-1)
	if got := d.nextPick(hits); got != hits[0] {
		t.Errorf("with nothing selected, nextPick = %d, want %d", got, hits[0])
	}
	for i := range hits {
		d.setSelected(hits[i])
		want := hits[(i+1)%len(hits)]
		if got := d.nextPick(hits); got != want {
			t.Errorf("after hit %d, nextPick = %d, want %d", i, got, want)
		}
	}
}
