package main

import (
	"math"
	"testing"

	"github.com/AndreRenaud/stepview/internal/step"
)

func TestProjectCullsBackFaces(t *testing.T) {
	d := loadDoc(t, "block.stp")
	o := defaultOrbit()
	o.target = d.bounds.Center()
	o.dist = d.bounds.Diag() * 3
	c := o.camera(800, 600)
	var r renderer
	verts, idx := r.project(d, &c)
	// From a general direction, three of the block's six sides face the
	// camera.
	if got := len(idx) / 3; got != d.triangles/2 {
		t.Errorf("drew %d triangles, want %d", got, d.triangles/2)
	}
	for _, i := range idx {
		if int(i) >= len(verts) {
			t.Fatalf("index %d out of range of %d vertices", i, len(verts))
		}
	}
	for _, v := range verts {
		if v.Custom0 < 0 || v.Custom0 > 1 {
			t.Fatalf("depth %v outside [0, 1]", v.Custom0)
		}
	}
}

func TestProjectClipsAtNearPlane(t *testing.T) {
	d := loadDoc(t, "block.stp")
	b := d.bounds
	// Hover just above the middle of the top face, looking across it: the
	// face extends behind the camera and must be cut, not dropped.
	o := defaultOrbit()
	o.target = step.Vec3{X: (b.Min.X + b.Max.X) / 2, Y: (b.Min.Y + b.Max.Y) / 2, Z: b.Max.Z + b.Diag()*1e-3}
	o.dist = 1e-6
	o.pitch = 10 * math.Pi / 180
	c := o.camera(800, 600)
	// Vertices that project gives no depth, those behind the camera, keep
	// this mark.
	for i := range d.verts {
		d.verts[i].Custom0 = -1
	}
	var r renderer
	verts, idx := r.project(d, &c)
	clipped := 0
	for _, wk := range r.work {
		clipped += len(wk.extraI) / 3
	}
	if clipped == 0 {
		t.Fatal("no triangles were clipped")
	}
	for _, i := range idx {
		if int(i) >= len(verts) {
			t.Fatalf("index %d out of range of %d vertices", i, len(verts))
		}
		v := verts[i]
		if v.Custom0 < 0 || v.Custom0 > 1 || math.IsNaN(float64(v.DstX)) || math.IsNaN(float64(v.DstY)) {
			t.Fatalf("vertex behind the camera or invalid: %+v", v)
		}
	}
}
