package meshload

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AndreRenaud/stepview/internal/step"
)

// checkModel verifies the invariants the viewer relies on.
func checkModel(t testing.TB, m *step.Model) {
	t.Helper()
	triangles := 0
	checked := map[*step.Mesh]bool{}
	var walk func(n *step.Node)
	walk = func(n *step.Node) {
		if me := n.Mesh; me != nil && !checked[me] {
			checked[me] = true
			nv := len(me.Positions) / 3
			if len(me.Normals) != len(me.Positions) || len(me.Colors) != len(me.Positions) {
				t.Fatalf("%s: attribute length mismatch", n.Name)
			}
			if len(me.Indices)%3 != 0 {
				t.Fatalf("%s: %d indices", n.Name, len(me.Indices))
			}
			checkUVs(t, n.Name, me)
			for _, i := range me.Indices {
				if int(i) >= nv {
					t.Fatalf("%s: index %d out of range (%d vertices)", n.Name, i, nv)
				}
			}
			for i := range nv {
				nl := math.Sqrt(float64(me.Normals[i*3]*me.Normals[i*3] + me.Normals[i*3+1]*me.Normals[i*3+1] + me.Normals[i*3+2]*me.Normals[i*3+2]))
				if math.IsNaN(nl) || math.Abs(nl-1) > 1e-3 {
					t.Fatalf("%s: vertex %d normal length %g", n.Name, i, nl)
				}
				for k := range 3 {
					if v := float64(me.Positions[i*3+k]); math.IsNaN(v) || math.IsInf(v, 0) {
						t.Fatalf("%s: vertex %d has invalid position", n.Name, i)
					}
				}
			}
		}
		if n.Mesh != nil {
			triangles += n.Mesh.TriangleCount()
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, r := range m.Roots {
		walk(r)
	}
	if triangles != m.Stats.Triangles {
		t.Fatalf("Stats.Triangles = %d, tree has %d", m.Stats.Triangles, triangles)
	}
}

// checkUVs verifies that a mesh has texture coordinates exactly when it
// has a texture.
func checkUVs(t testing.TB, name string, me *step.Mesh) {
	t.Helper()
	if me.Texture == nil {
		if len(me.UVs) != 0 {
			t.Fatalf("%s: %d UVs without a texture", name, len(me.UVs))
		}
		return
	}
	if me.Texture.Image == nil {
		t.Fatalf("%s: texture has no image", name)
	}
	if len(me.UVs) != len(me.Positions)/3*2 {
		t.Fatalf("%s: %d UVs for %d vertices", name, len(me.UVs), len(me.Positions)/3)
	}
	for _, v := range me.UVs {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			t.Fatalf("%s: invalid UV %g", name, v)
		}
	}
}

// worldBox is the bounds of the whole model in world space.
func worldBox(m *step.Model) step.Box {
	b := step.EmptyBox()
	var walk func(n *step.Node, xf step.Affine)
	walk = func(n *step.Node, xf step.Affine) {
		xf = xf.Mul(n.Local)
		if n.Mesh != nil {
			b.Union(n.Mesh.Bounds.Transform(xf))
		}
		for _, c := range n.Children {
			walk(c, xf)
		}
	}
	for _, r := range m.Roots {
		walk(r, step.Identity())
	}
	return b
}

func near(a, b step.Vec3) bool { return a.Sub(b).Len() < 1e-4 }

// sampleFiles lists the test data files with the given extension, at the
// top level or in a directory of their own (with their textures).
func sampleFiles(ext string) []string {
	top, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "*"+ext))
	sub, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "*", "*"+ext))
	return append(top, sub...)
}

func TestLoadSamples(t *testing.T) {
	var paths []string
	for _, ext := range []string{".obj", ".stl", ".3mf", ".3ds"} {
		paths = append(paths, sampleFiles(ext)...)
	}
	if len(paths) == 0 {
		t.Fatal("no test data")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			m, err := LoadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if m.Stats.Triangles == 0 {
				t.Fatal("no triangles")
			}
			checkModel(t, m)
			if textured[filepath.Base(path)] && !hasTexture(m) {
				t.Errorf("no textures (warnings %q)", m.Warnings)
			}
		})
	}
}

// textured lists the samples that should load with textures.
var textured = map[string]bool{
	"spot.obj":                      true,
	"cube_with_diffuse_texture.3ds": true,
	"sphere_logo.3mf":               true,
}

func hasTexture(m *step.Model) bool {
	var walk func(n *step.Node) bool
	walk = func(n *step.Node) bool {
		if n.Mesh != nil && n.Mesh.Texture != nil {
			return true
		}
		return slices.ContainsFunc(n.Children, walk)
	}
	return slices.ContainsFunc(m.Roots, walk)
}

func TestSampleDetails(t *testing.T) {
	load := func(name string) *step.Model {
		t.Helper()
		m, err := LoadFile(filepath.Join("..", "..", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	// A binary STL whose header starts with "solid" and has a stray
	// trailing byte.
	if m := load("half_donut_solid_header.stl"); m.Stats.Triangles != 288 {
		t.Errorf("half donut: %d triangles, want 288", m.Stats.Triangles)
	}
	// Components reuse one mesh with different transforms.
	m := load("components.3mf")
	obj := m.Roots[0].Children[0]
	if len(obj.Children) != 2 || obj.Children[0].Mesh != obj.Children[1].Mesh {
		t.Errorf("components: want two instances of one mesh, got %+v", obj.Children)
	}
	if b := worldBox(m); !near(b.Max, step.Vec3{X: 70, Y: 60, Z: 30}) {
		t.Errorf("components: bounds %v", b)
	}
	// Per-triangle colours from a colour group.
	m = load("rhombicuboctahedron_color.3mf")
	colours := map[[3]float32]bool{}
	me := m.Roots[0].Children[0].Mesh
	for i := 0; i < len(me.Colors); i += 3 {
		colours[[3]float32(me.Colors[i:i+3])] = true
	}
	if len(colours) != 5 {
		t.Errorf("rhombicuboctahedron: %d colours, want 5", len(colours))
	}
	// OBJ groups and MTL colours: the light is white, the left wall red.
	m = load("cornell_box.obj")
	byName := map[string]*step.Node{}
	for _, c := range m.Roots[0].Children {
		byName[c.Name] = c
	}
	if c := byName["rightWall"]; c == nil || c.Mesh.Colors[1] < 0.3 || c.Mesh.Colors[0] > 0.3 {
		t.Errorf("cornell box: right wall should be green")
	}
	// The file is Y up; the room's 2 unit height ends up along Z.
	if b := worldBox(m); math.Abs(b.Max.Z-1.99) > 0.01 || b.Min.Z != 0 {
		t.Errorf("cornell box: bounds %v", b)
	}
}

func TestSTLInsideOut(t *testing.T) {
	// A unit cube written as an ASCII STL, with every triangle wound
	// inwards. The loader should turn it the right way out.
	corners := func(i int) [3]float32 {
		return [3]float32{float32(i & 1), float32(i >> 1 & 1), float32(i >> 2 & 1)}
	}
	quads := [][4]int{{0, 2, 3, 1}, {4, 5, 7, 6}, {0, 1, 5, 4}, {2, 6, 7, 3}, {0, 4, 6, 2}, {1, 3, 7, 5}}
	var sb strings.Builder
	sb.WriteString("solid cube\n")
	for _, q := range quads {
		for _, tri := range [][3]int{{q[0], q[2], q[1]}, {q[0], q[3], q[2]}} { // reversed
			sb.WriteString("facet normal 0 0 0\nouter loop\n")
			for _, v := range tri {
				p := corners(v)
				sb.WriteString("vertex " + ftoa(p[0]) + " " + ftoa(p[1]) + " " + ftoa(p[2]) + "\n")
			}
			sb.WriteString("endloop\nendfacet\n")
		}
	}
	sb.WriteString("endsolid cube\n")
	m, err := loadSTL([]byte(sb.String()), "cube")
	if err != nil {
		t.Fatal(err)
	}
	checkModel(t, m)
	me := m.Roots[0].Mesh
	if me.TriangleCount() != 12 {
		t.Fatalf("%d triangles", me.TriangleCount())
	}
	// Every normal should point away from the centre, and creases keep the
	// faces flat: 6 faces x 4 corners.
	if n := len(me.Positions) / 3; n != 24 {
		t.Errorf("%d vertices, want 24", n)
	}
	for i := 0; i < len(me.Positions); i += 3 {
		p := step.Vec3{X: float64(me.Positions[i]) - 0.5, Y: float64(me.Positions[i+1]) - 0.5, Z: float64(me.Positions[i+2]) - 0.5}
		n := step.Vec3{X: float64(me.Normals[i]), Y: float64(me.Normals[i+1]), Z: float64(me.Normals[i+2])}
		if p.Dot(n) <= 0 {
			t.Fatalf("vertex %v has inward normal %v", p, n)
		}
	}
}

func ftoa(f float32) string {
	if f == 0 {
		return "0"
	}
	return "1"
}

func binarySTL(header string, tris [][3][3]float32, attr uint16) []byte {
	var buf bytes.Buffer
	h := make([]byte, 80)
	copy(h, header)
	buf.Write(h)
	binary.Write(&buf, binary.LittleEndian, uint32(len(tris)))
	for _, t := range tris {
		binary.Write(&buf, binary.LittleEndian, [3]float32{})
		binary.Write(&buf, binary.LittleEndian, t)
		binary.Write(&buf, binary.LittleEndian, attr)
	}
	return buf.Bytes()
}

func TestBinarySTLColours(t *testing.T) {
	tri := [][3][3]float32{{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}}}
	colourOf := func(data []byte) [3]float32 {
		m, err := loadSTL(data, "t")
		if err != nil {
			t.Fatal(err)
		}
		return [3]float32(m.Roots[0].Mesh.Colors[:3])
	}
	// VisCAM: bit 15 set, red in the high bits.
	if c := colourOf(binarySTL("solid but binary", tri, 0x8000|31<<10)); c != [3]float32{1, 0, 0} {
		t.Errorf("VisCAM colour = %v", c)
	}
	// Magics: bit 15 clear, red in the low bits.
	if c := colourOf(binarySTL("COLOR=\xff\x00\x00\xff", tri, 31<<10)); c != [3]float32{0, 0, 1} {
		t.Errorf("Magics colour = %v", c)
	}
	// Magics default from the header.
	if c := colourOf(binarySTL("COLOR=\x00\xff\x00\xff", tri, 0x8000)); c != [3]float32{0, 1, 0} {
		t.Errorf("Magics default colour = %v", c)
	}
	if c := colourOf(binarySTL("plain", tri, 0)); c != defaultColour {
		t.Errorf("default colour = %v", c)
	}
}

func TestOBJ(t *testing.T) {
	obj := `mtllib a b.mtl
v 0 0 0
v 1 0 0
v 1 1 0
v 0 1 0
v 0 0 1 1 0 0
vn 0 0 1
o first
usemtl blue
f 1//1 2//1 3//1 4//1
o second
usemtl missing
f -5 -4 -1
f 1 2 99
`
	mtl := "newmtl blue # comment\nKd 0 0 1\n"
	m, err := loadOBJ([]byte(obj), "t", func(name string) ([]byte, error) {
		if name != "a b.mtl" {
			return nil, os.ErrNotExist
		}
		return []byte(mtl), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	checkModel(t, m)
	root := m.Roots[0]
	if len(root.Children) != 2 || root.Children[0].Name != "first" || root.Children[1].Name != "second" {
		t.Fatalf("unexpected tree: %+v", root.Children)
	}
	first, second := root.Children[0].Mesh, root.Children[1].Mesh
	if first.TriangleCount() != 2 || [3]float32(first.Colors[:3]) != [3]float32{0, 0, 1} {
		t.Errorf("first: %d triangles, colour %v", first.TriangleCount(), first.Colors[:3])
	}
	// The vertex colour on the fifth vertex wins over the material.
	found := false
	for i := 0; i < len(second.Colors); i += 3 {
		if [3]float32(second.Colors[i:i+3]) == [3]float32{1, 0, 0} {
			found = true
		}
	}
	if second.TriangleCount() != 1 || !found {
		t.Errorf("second: %d triangles, colours %v", second.TriangleCount(), second.Colors)
	}
	if len(m.Warnings) != 2 {
		t.Errorf("warnings = %q", m.Warnings)
	}
}

// make3MF builds a 3MF package from part name -> content.
func make3MF(t testing.TB, parts map[string]string) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range parts {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(content))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const tmfRels = `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Target="/3D/main.model" Id="rel0" Type="http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel"/>
</Relationships>`

const tmfRoot = `<?xml version="1.0" encoding="UTF-8"?>
<model unit="centimeter" xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02"
 xmlns:p="http://schemas.microsoft.com/3dmanufacturing/production/2015/06">
<resources>
<basematerials id="1"><base name="red" displaycolor="#FF0000"/><base name="green" displaycolor="#00FF00FF"/></basematerials>
<object id="2" name="Assembly"><components>
<component objectid="7" p:path="/3D/Objects/part.model" transform="1 0 0 0 1 0 0 0 1 10 0 0"/>
</components></object>
</resources>
<build><item objectid="2" transform="0 1 0 -1 0 0 0 0 1 0 0 0"/></build>
</model>`

const tmfPart = `<?xml version="1.0" encoding="UTF-8"?>
<model unit="centimeter" xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02">
<resources>
<basematerials id="1"><base name="blue" displaycolor="#0000FF"/></basematerials>
<object id="7" name="Triangle" pid="1" pindex="0"><mesh>
<vertices><vertex x="0" y="0" z="0"/><vertex x="1" y="0" z="0"/><vertex x="0" y="1" z="0"/></vertices>
<triangles><triangle v1="0" v2="1" v3="2"/><triangle v1="0" v2="1" v3="9"/></triangles>
</mesh></object>
</resources>
</model>`

func Test3MF(t *testing.T) {
	data := make3MF(t, map[string]string{
		"_rels/.rels":              tmfRels,
		"3D/main.model":            tmfRoot,
		"3D/Objects/part.model":    tmfPart,
		"[Content_Types].xml":      "",
		"Metadata/thumbnail.png":   "",
		"3D/Objects/unused.model":  "not xml",
		"3D/_rels/main.model.rels": "",
	})
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	m, err := load3MF(zr, "t")
	if err != nil {
		t.Fatal(err)
	}
	checkModel(t, m)
	asm := m.Roots[0].Children[0]
	if asm.Name != "Assembly" || len(asm.Children) != 1 || asm.Children[0].Name != "Triangle" {
		t.Fatalf("unexpected tree: %+v", asm)
	}
	me := asm.Children[0].Mesh
	if me.TriangleCount() != 1 || [3]float32(me.Colors[:3]) != [3]float32{0, 0, 1} {
		t.Errorf("triangle: %d triangles, colour %v", me.TriangleCount(), me.Colors[:3])
	}
	// Component moves +10 cm in x, then the item rotates +90 degrees about
	// z (row-vector matrices); centimetres become millimetres.
	b := worldBox(m)
	if !near(b.Min, step.Vec3{X: -10, Y: 100, Z: 0}) || !near(b.Max, step.Vec3{X: 0, Y: 110, Z: 0}) {
		t.Errorf("bounds = %v", b)
	}
	if len(m.Warnings) != 1 {
		t.Errorf("warnings = %q", m.Warnings)
	}
}
