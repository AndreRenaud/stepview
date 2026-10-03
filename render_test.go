package main

import (
	"image"
	"image/color"
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

// quadMesh is a unit square in the XY plane at height z, facing up, with
// texture coordinates matching x and y when tex is set.
func quadMesh(z float32, tex *step.Texture) *step.Mesh {
	m := &step.Mesh{
		Positions:  []float32{0, 0, z, 1, 0, z, 1, 1, z, 0, 1, z},
		Normals:    []float32{0, 0, 1, 0, 0, 1, 0, 0, 1, 0, 0, 1},
		Colors:     []float32{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
		Indices:    []uint32{0, 1, 2, 0, 2, 3},
		FaceStarts: []uint32{0},
		Bounds:     step.Box{Max: step.Vec3{X: 1, Y: 1, Z: float64(z)}, Min: step.Vec3{Z: float64(z)}},
	}
	if tex != nil {
		m.Texture = tex
		m.UVs = []float32{0, 0, 1, 0, 1, 1, 0, 1}
	}
	return m
}

func TestProjectGroupsByTexture(t *testing.T) {
	a := &step.Texture{Name: "a", Image: image.NewRGBA(image.Rect(0, 0, 2, 2))}
	b := &step.Texture{Name: "b", Image: image.NewRGBA(image.Rect(0, 0, 4, 4)), Cutout: true}
	root := &step.Node{Name: "root", Local: step.Identity()}
	for i, tex := range []*step.Texture{a, nil, b, a, nil} {
		root.Children = append(root.Children, &step.Node{Name: "q", Local: step.Identity(), Mesh: quadMesh(float32(i), tex)})
	}
	d := newDocument("quads", &step.Model{Roots: []*step.Node{root}})
	o := defaultOrbit()
	o.target = d.bounds.Center()
	o.dist = d.bounds.Diag() * 3
	c := o.camera(800, 600)
	var r renderer
	verts, idx := r.project(d, &c)
	if len(idx) != 5*6 {
		t.Fatalf("drew %d triangles, want 10", len(idx)/3)
	}
	// One range per texture, untextured first, covering everything.
	if len(r.ranges) != 3 || r.ranges[0].tex != -1 || r.ranges[1].tex == r.ranges[2].tex {
		t.Fatalf("ranges = %+v", r.ranges)
	}
	if r.ranges[0].vStart != 0 || r.ranges[2].vEnd != len(verts) || r.ranges[2].iEnd != len(idx) {
		t.Fatalf("ranges do not cover the output: %+v", r.ranges)
	}
	for k, rg := range r.ranges {
		if k > 0 && (rg.vStart != r.ranges[k-1].vEnd || rg.iStart != r.ranges[k-1].iEnd) {
			t.Fatalf("ranges not contiguous: %+v", r.ranges)
		}
		// Two quads have no texture, two texture a and one texture b
		// (the cutout one).
		want := 12
		if rg.tex >= 0 && r.textures[rg.tex].cutout {
			want = 6
		}
		if rg.iEnd-rg.iStart != want {
			t.Errorf("range %+v has %d indices, want %d", rg, rg.iEnd-rg.iStart, want)
		}
		for _, i := range r.indices[rg.iStart:rg.iEnd] {
			if int(i) >= rg.vEnd-rg.vStart {
				t.Fatalf("index %d outside range %+v", i, rg)
			}
		}
		// Textured vertices carry their coordinates divided by depth:
		// multiplied by the decoded depth they are back in [0, 1].
		for _, v := range verts[rg.vStart:rg.vEnd] {
			if rg.tex < 0 {
				continue
			}
			invz := v.Custom0*r.depthA + r.depthB
			u, w := v.SrcX/invz, v.SrcY/invz
			if u < -1e-4 || u > 1+1e-4 || w < -1e-4 || w > 1+1e-4 {
				t.Fatalf("texture coordinates (%g, %g) out of range", u, w)
			}
		}
	}
	if len(r.textures) != 2 {
		t.Errorf("uploaded %d textures, want 2", len(r.textures))
	}
}

func TestTexturePixels(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	src.SetNRGBA(0, 0, color.NRGBA{200, 100, 50, 255})
	src.SetNRGBA(1, 0, color.NRGBA{200, 100, 50, 0}) // colour under a hole
	// Without holes, alpha is ignored and the colour kept.
	px := texturePixels(src, false)
	if got := px.RGBAAt(1, 0); got != (color.RGBA{200, 100, 50, 255}) {
		t.Errorf("opaque texture: transparent pixel became %v", got)
	}
	// With holes, the pixels are premultiplied for the GPU.
	src.SetNRGBA(1, 0, color.NRGBA{200, 100, 50, 128})
	px = texturePixels(src, true)
	if got := px.RGBAAt(1, 0); got.A != 128 || got.R != 100 || got.G != 50 {
		t.Errorf("cutout texture: half transparent pixel became %v", got)
	}
	if got := px.RGBAAt(0, 0); got != (color.RGBA{200, 100, 50, 255}) {
		t.Errorf("cutout texture: opaque pixel became %v", got)
	}
}
