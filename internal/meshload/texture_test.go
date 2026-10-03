package meshload

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndreRenaud/stepview/internal/step"
)

// testPNG encodes a w x h image whose pixels are given by fn.
func testPNG(t testing.TB, w, h int, fn func(x, y int) color.NRGBA) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, fn(x, y))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func opaquePNG(t testing.TB) []byte {
	return testPNG(t, 4, 4, func(x, y int) color.NRGBA { return color.NRGBA{255, uint8(x * 60), uint8(y * 60), 255} })
}

// halfPNG is black on the left and white on the right.
func halfPNG(t testing.TB) []byte {
	return testPNG(t, 4, 4, func(x, y int) color.NRGBA {
		if x < 2 {
			return color.NRGBA{0, 0, 0, 255}
		}
		return color.NRGBA{255, 255, 255, 255}
	})
}

// holesPNG is transparent in its left column and opaque dark grey
// elsewhere.
func holesPNG(t testing.TB) []byte {
	return testPNG(t, 4, 4, func(x, y int) color.NRGBA {
		if x == 0 {
			return color.NRGBA{}
		}
		return color.NRGBA{20, 20, 20, 255}
	})
}

// files serves in-memory files by name, in place of dirOpener.
func files(m map[string][]byte) func(string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		if d, ok := m[name]; ok {
			return d, nil
		}
		return nil, fs.ErrNotExist
	}
}

// uvsByPosition maps each vertex position of a mesh to its UV.
func uvsByPosition(t *testing.T, me *step.Mesh) map[[3]float32][2]float32 {
	t.Helper()
	out := map[[3]float32][2]float32{}
	for i := range len(me.Positions) / 3 {
		p, uv := [3]float32(me.Positions[i*3:i*3+3]), [2]float32(me.UVs[i*2:i*2+2])
		if old, ok := out[p]; ok && old != uv {
			t.Fatalf("position %v has UVs %v and %v", p, old, uv)
		}
		out[p] = uv
	}
	return out
}

func allWhite(me *step.Mesh) bool {
	for _, c := range me.Colors {
		if c != 1 {
			return false
		}
	}
	return true
}

func TestDecodeTexture(t *testing.T) {
	tex, err := DecodeTexture(opaquePNG(t), "a.png")
	if err != nil {
		t.Fatal(err)
	}
	if tex.Name != "a.png" || tex.Cutout || tex.Image.Bounds().Dx() != 4 {
		t.Errorf("opaque: %+v", tex)
	}
	// Alpha alone does not make holes.
	if tex, err := DecodeTexture(holesPNG(t), "b.png"); err != nil || tex.Cutout {
		t.Errorf("holes: %+v, %v", tex, err)
	}
	// Scaled down to fit.
	wide := testPNG(t, step.MaxTextureSize+100, 2, func(x, y int) color.NRGBA { return color.NRGBA{0, 0, 0, 255} })
	if tex, err := DecodeTexture(wide, "c.png"); err != nil || tex.Image.Bounds().Dx() != step.MaxTextureSize || tex.Image.Bounds().Dy() != 1 {
		t.Errorf("wide: %v, %v", tex.Image.Bounds(), err)
	}
	if _, err := DecodeTexture([]byte("not an image"), "d.png"); err == nil {
		t.Error("decoded garbage")
	}
	// A PNG header claiming a huge image is rejected before decoding.
	var hdr bytes.Buffer
	hdr.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := []byte("IHDR\x00\x00\x40\x00\x00\x00\x40\x00\x08\x06\x00\x00\x00")
	binary.Write(&hdr, binary.BigEndian, uint32(len(ihdr)-4))
	hdr.Write(ihdr)
	binary.Write(&hdr, binary.BigEndian, crc32.ChecksumIEEE(ihdr))
	if _, err := DecodeTexture(hdr.Bytes(), "e.png"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("huge PNG: %v", err)
	}
}

func TestDecodeTGA(t *testing.T) {
	// 2x1, 32 bits, top down, 8 alpha bits: blue opaque, red transparent.
	raw := append([]byte{0, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0, 1, 0, 32, 0x28}, 255, 0, 0, 255, 0, 0, 255, 0)
	tex, err := DecodeTexture(raw, "a.tga")
	if err != nil {
		t.Fatal(err)
	}
	if c := color.NRGBAModel.Convert(tex.Image.At(0, 0)).(color.NRGBA); c != (color.NRGBA{0, 0, 255, 255}) {
		t.Errorf("pixel 0 = %v", c)
	}
	if c := color.NRGBAModel.Convert(tex.Image.At(1, 0)).(color.NRGBA); c.A != 0 || tex.Cutout {
		t.Errorf("pixel 1 = %v, cutout %v", c, tex.Cutout)
	}
	// Run length encoded, 24 bits, bottom up: one run of green on 2x2.
	rle := append([]byte{0, 0, 10, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0, 2, 0, 24, 0}, 0x83, 0, 255, 0)
	if tex, err := DecodeTexture(rle, "b.tga"); err != nil || color.NRGBAModel.Convert(tex.Image.At(1, 1)) != (color.NRGBA{0, 255, 0, 255}) {
		t.Errorf("RLE: %v", err)
	}
	if _, err := DecodeTexture(rle[:20], "c.tga"); err == nil {
		t.Error("decoded truncated TGA")
	}
}

func TestWithMask(t *testing.T) {
	tex, err := DecodeTexture(opaquePNG(t), "a.png")
	if err != nil {
		t.Fatal(err)
	}
	mask, err := DecodeTexture(testPNG(t, 2, 1, func(x, y int) color.NRGBA { return color.NRGBA{uint8(255 * x), 0, 0, 255} }), "m.png")
	if err != nil {
		t.Fatal(err)
	}
	// An opaque mask's brightness is the alpha; red's is 30% (ITU-R 601).
	m := WithMask(tex, mask.Image)
	if !m.Cutout || m.Name != "a.png" || tex.Cutout {
		t.Errorf("masked: %+v", m)
	}
	if _, _, _, a := m.Image.At(0, 0).RGBA(); a != 0 {
		t.Errorf("left alpha = %d", a)
	}
	if _, _, _, a := m.Image.At(3, 0).RGBA(); a < 0x4000 || a > 0x5800 {
		t.Errorf("right alpha = %d", a)
	}
	// A mask with an alpha channel gives its alpha: dark opaque pixels stay
	// opaque.
	holes, err := DecodeTexture(holesPNG(t), "h.png")
	if err != nil {
		t.Fatal(err)
	}
	m = WithMask(holes, holes.Image)
	if !m.Cutout {
		t.Errorf("self masked: %+v", m)
	}
	for x, want := range []uint32{0, 0xffff, 0xffff, 0xffff} {
		if _, _, _, a := m.Image.At(x, 2).RGBA(); a != want {
			t.Errorf("self masked alpha at %d = %#x, want %#x", x, a, want)
		}
	}
}

func TestFindFile(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"Maps/Label.JPG", "sub/a b.png", "top.png"} {
		p = filepath.Join(dir, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for ref, want := range map[string]string{
		`C:\models\maps\LABEL.jpg`: "Maps/Label.JPG",
		"sub/a b.png":              "sub/a b.png",
		`sub\a b.png`:              "sub/a b.png",
		"/elsewhere/top.png":       "top.png",
		"TOP.PNG":                  "top.png",
		"missing.png":              "",
		"sub/":                     "",
	} {
		got, ok := findFile(dir, ref)
		if want == "" {
			if ok {
				t.Errorf("%q: found %s", ref, got)
			}
			continue
		}
		// Case insensitive file systems find the name as given.
		if !ok || !strings.EqualFold(got, filepath.Join(dir, filepath.FromSlash(want))) {
			t.Errorf("%q: got %s, %v", ref, got, ok)
		}
	}
}

func TestBuilderTextures(t *testing.T) {
	// An inside-out unit cube: sides 0 and 1 use texture a, 2 and 3 none,
	// 4 and 5 texture b. UVs are a function of position, so they must
	// still match after the faces are turned the right way out.
	a, b := &step.Texture{Name: "a", Image: image.NewGray(image.Rect(0, 0, 1, 1))}, &step.Texture{Name: "b", Image: image.NewGray(image.Rect(0, 0, 1, 1))}
	uvOf := func(p [3]float32) [2]float32 { return [2]float32{p[0] + 2*p[1], p[2]} }
	corners := func(i int) [3]float32 {
		return [3]float32{float32(i & 1), float32(i >> 1 & 1), float32(i >> 2 & 1)}
	}
	quads := [][4]int{{0, 2, 3, 1}, {4, 5, 7, 6}, {0, 1, 5, 4}, {2, 6, 7, 3}, {0, 4, 6, 2}, {1, 3, 7, 5}}
	var bl builder
	white := [3][3]float32{{1, 1, 1}, {1, 1, 1}, {1, 1, 1}}
	for s, q := range quads {
		for _, tri := range [][3]int{{q[0], q[2], q[1]}, {q[0], q[3], q[2]}} { // reversed
			var p [3][3]float32
			var uv [3][2]float32
			for k, v := range tri {
				p[k] = corners(v)
				uv[k] = uvOf(p[k])
			}
			tex := []*step.Texture{a, a, nil, nil, b, b}[s]
			bl.addTextured(p, white, nil, tex, &uv, nil)
		}
	}
	ms := bl.meshes()
	if len(ms) != 3 || ms[0].Texture != a || ms[1].Texture != nil || ms[2].Texture != b {
		t.Fatalf("meshes: %+v", ms)
	}
	for _, me := range ms {
		checkUVs(t, "cube", me)
		if me.TriangleCount() != 4 || len(me.Positions)/3 != 8 {
			t.Errorf("%d triangles, %d vertices", me.TriangleCount(), len(me.Positions)/3)
		}
		for i := 0; i < len(me.Positions); i += 3 {
			p := [3]float32(me.Positions[i : i+3])
			n := step.Vec3{X: float64(me.Normals[i]), Y: float64(me.Normals[i+1]), Z: float64(me.Normals[i+2])}
			if vec(p).Sub(step.Vec3{X: 0.5, Y: 0.5, Z: 0.5}).Dot(n) <= 0 {
				t.Fatalf("vertex %v has inward normal %v", p, n)
			}
		}
		if me.Texture == nil {
			continue
		}
		for p, uv := range uvsByPosition(t, me) {
			if uv != uvOf(p) {
				t.Errorf("vertex %v has UV %v, want %v", p, uv, uvOf(p))
			}
		}
	}

	// Corners that share a position but not a UV stay separate; invalid
	// UVs become zero.
	var q builder
	nan := float32(math.NaN())
	q.addTextured([3][3]float32{{0, 0, 0}, {1, 0, 0}, {1, 1, 0}}, white, nil, a, &[3][2]float32{{0, 0}, {1, 0}, {1, 1}}, nil)
	q.addTextured([3][3]float32{{0, 0, 0}, {1, 1, 0}, {0, 1, 0}}, white, nil, a, &[3][2]float32{{5, 5}, {1, 1}, {nan, 0}}, nil)
	ms = q.meshes()
	if len(ms) != 1 || len(ms[0].Positions)/3 != 5 {
		t.Fatalf("seam: %+v", ms)
	}
	checkUVs(t, "seam", ms[0])
}

func TestOBJTextures(t *testing.T) {
	obj := `mtllib m.mtl
v 0 0 0
v 1 0 0
v 1 1 0
v 0 1 0
vt 0 0
vt 1 0
vt 1 1
vt 0 1
vt 0.25 0.75
vn 0 0 1
o textured
usemtl tex
f 1/1/1 2/2/1 3/3/1 4/4/1
usemtl plain
f 1 2 3
usemtl tex
f 1//1 3//1 4//1
o masked
usemtl masked
f -4/-5 -3/-4 -2/-3
o missing
usemtl gone
f 1/5 2/5 3/5
o self
usemtl self
f 1/1 2/2 3/3
`
	mtl := `newmtl tex
Kd 0 0 0
map_Kd -s 2 1 -o 0.5 0 -bm 0.5 -clamp on my tex.png
newmtl plain
Kd 1 0 0
newmtl masked
Kd 0.5 0.5 0.5
map_Kd my tex.png
map_d -imfchan l mask.png
newmtl gone
Kd 0 1 0
map_Kd nothere.png
newmtl self
map_Kd my tex.png
map_d my tex.png
`
	m, err := loadOBJ([]byte(obj), "t", files(map[string][]byte{
		"m.mtl":      []byte(mtl),
		"my tex.png": holesPNG(t),
		"mask.png":   halfPNG(t),
	}))
	if err != nil {
		t.Fatal(err)
	}
	checkModel(t, m)
	if len(m.Warnings) != 1 || !strings.Contains(m.Warnings[0], "nothere.png") {
		t.Errorf("warnings = %q", m.Warnings)
	}
	root := m.Roots[0]
	if len(root.Children) != 4 {
		t.Fatalf("unexpected tree: %+v", root.Children)
	}
	textured, masked, missing, self := root.Children[0], root.Children[1], root.Children[2], root.Children[3]
	if textured.Mesh != nil || len(textured.Children) != 2 || textured.Children[0].Name != "textured.0" || textured.Children[1].Name != "textured.1" {
		t.Fatalf("textured: %+v", textured)
	}
	tm, plain := textured.Children[0].Mesh, textured.Children[1].Mesh
	// Without map_d, the texture's own alpha does not cut holes.
	if tm.Texture == nil || tm.Texture.Name != "my tex.png" || tm.Texture.Cutout || !allWhite(tm) || tm.TriangleCount() != 2 {
		t.Errorf("textured mesh: %+v", tm)
	}
	// -s 2 1 -o 0.5 0, and V flipped to a top left origin.
	want := map[[3]float32][2]float32{{0, 0, 0}: {0.5, 1}, {1, 0, 0}: {2.5, 1}, {1, 1, 0}: {2.5, 0}, {0, 1, 0}: {0.5, 0}}
	if got := uvsByPosition(t, tm); len(got) != 4 {
		t.Errorf("UVs = %v", got)
	} else {
		for p, uv := range want {
			if got[p] != uv {
				t.Errorf("UV at %v = %v, want %v", p, got[p], uv)
			}
		}
	}
	// The untextured faces keep their material colours: red, and black for
	// the face of the textured material that has no texture coordinates.
	colours := map[[3]float32]int{}
	for i := 0; i < len(plain.Colors); i += 3 {
		colours[[3]float32(plain.Colors[i:i+3])]++
	}
	if plain.Texture != nil || plain.TriangleCount() != 2 || colours[[3]float32{1, 0, 0}] != 3 || colours[[3]float32{}] != 3 {
		t.Errorf("plain mesh: %d triangles, colours %v", plain.TriangleCount(), colours)
	}
	mm := masked.Mesh
	if mm == nil || mm.Texture == nil || mm.Texture == tm.Texture || !mm.Texture.Cutout || !allWhite(mm) {
		t.Fatalf("masked: %+v", masked)
	}
	if _, _, _, a := mm.Texture.Image.At(0, 0).RGBA(); a != 0 {
		t.Errorf("masked alpha = %d", a)
	}
	if uv := uvsByPosition(t, mm)[[3]float32{1, 0, 0}]; uv != [2]float32{1, 1} {
		t.Errorf("masked UV = %v", uv)
	}
	if me := missing.Mesh; me == nil || me.Texture != nil || [3]float32(me.Colors[:3]) != [3]float32{0, 1, 0} {
		t.Errorf("missing: %+v", missing)
	}
	// map_d naming the texture itself uses its alpha channel.
	if me := self.Mesh; me == nil || me.Texture == nil || !me.Texture.Cutout {
		t.Fatalf("self: %+v", self)
	} else if _, _, _, a := me.Texture.Image.At(1, 0).RGBA(); a != 0xffff {
		t.Errorf("self alpha = %#x", a)
	}
}

func TestParseMap(t *testing.T) {
	for line, want := range map[string]string{
		"a.png":                          "a.png",
		"-clamp on -bm 0.2 a b.png":      "a b.png",
		"-s 1 2 3 -o 1 -t 0 0 tex.jpg":   "tex.jpg",
		"-mm 0 1 -imfchan r  x\\y z.tga": "x\\y z.tga",
		"-o 3":                           "3",
		"":                               "",
	} {
		if name, _, _ := parseMap(strings.Fields(line)); name != want {
			t.Errorf("%q: name %q, want %q", line, name, want)
		}
	}
	if _, s, o := parseMap(strings.Fields("-s 2 -o 0.5 0.25 1 a.png")); s != [2]float32{2, 1} || o != [2]float32{0.5, 0.25} {
		t.Errorf("scale %v, offset %v", s, o)
	}
}

// texturedTDS is a quad whose faces use a textured and a plain material,
// and a copy of it without texture coordinates.
func texturedTDS() []byte {
	verts := []float32{0, 0, 0, 1, 0, 0, 1, 1, 0, 0, 1, 0}
	uvs := []float32{0, 0, 1, 0, 1, 1, 0, 1}
	faces := []uint16{0, 1, 2, 0, 0, 2, 3, 0}
	quad := func(name string, mapped bool) []byte {
		parts := []any{chunk(tdsVertices, uint16(4), verts)}
		if mapped {
			parts = append(parts, chunk(tdsUV, uint16(4), uvs))
		}
		parts = append(parts, chunk(tdsFaces, uint16(2), faces,
			chunk(tdsFaceMat, "Tex", uint16(1), uint16(0)),
			chunk(tdsFaceMat, "Plain", uint16(1), uint16(1)),
		))
		return chunk(tdsNamedObj, name, chunk(tdsTriMesh, parts...))
	}
	return chunk(tdsMain, chunk(tdsEditor,
		chunk(tdsMaterial,
			chunk(tdsMatName, "Tex"),
			chunk(tdsDiffuse, chunk(tdsColor24, []byte{0, 0, 0})),
			chunk(tdsTexMap, chunk(0x0030, uint16(100)), chunk(tdsMapFile, "LABEL.PNG"), chunk(tdsMapUScale, float32(2)), chunk(tdsMapVOffset, float32(0.25))),
			chunk(tdsOpacMap, chunk(tdsMapFile, "mask.png")),
		),
		chunk(tdsMaterial,
			chunk(tdsMatName, "Plain"),
			chunk(tdsDiffuse, chunk(tdsColor24, []byte{0, 0, 255})),
			chunk(tdsTexMap, chunk(tdsMapFile, "missing.png")),
		),
		quad("Box", true),
		quad("NoUV", false),
	))
}

func TestTDSTextures(t *testing.T) {
	m, err := loadTDS(texturedTDS(), "t", files(map[string][]byte{"LABEL.PNG": opaquePNG(t), "mask.png": halfPNG(t)}))
	if err != nil {
		t.Fatal(err)
	}
	checkModel(t, m)
	if len(m.Warnings) != 1 || !strings.Contains(m.Warnings[0], "missing.png") {
		t.Errorf("warnings = %q", m.Warnings)
	}
	root := m.Roots[0]
	if len(root.Children) != 2 {
		t.Fatalf("unexpected tree: %+v", root.Children)
	}
	box, nouv := root.Children[0], root.Children[1]
	if box.Mesh != nil || len(box.Children) != 2 || box.Children[0].Name != "Box.0" {
		t.Fatalf("box: %+v", box)
	}
	tm, plain := box.Children[0].Mesh, box.Children[1].Mesh
	if tm.Texture == nil || tm.Texture.Name != "LABEL.PNG" || !tm.Texture.Cutout || !allWhite(tm) {
		t.Errorf("textured: %+v", tm)
	}
	// U scaled by 2, V offset by 0.25, then flipped.
	want := map[[3]float32][2]float32{{0, 0, 0}: {0, 0.75}, {1, 0, 0}: {2, 0.75}, {1, 1, 0}: {2, -0.25}}
	got := uvsByPosition(t, tm)
	for p, uv := range want {
		if got[p] != uv {
			t.Errorf("UV at %v = %v, want %v", p, got[p], uv)
		}
	}
	if plain.Texture != nil || [3]float32(plain.Colors[:3]) != [3]float32{0, 0, 1} {
		t.Errorf("plain: %+v", plain)
	}
	// Without texture coordinates, the textured material's colour is used.
	if me := nouv.Mesh; me == nil || me.Texture != nil || len(nouv.Children) != 0 {
		t.Errorf("no UVs: %+v", nouv)
	}
}

const tmfTextured = `<?xml version="1.0" encoding="UTF-8"?>
<model unit="millimeter" xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02"
 xmlns:m="http://schemas.microsoft.com/3dmanufacturing/material/2015/02">
<resources>
<m:texture2d id="1" path="/3D/Texture/tex.png" contenttype="image/png" tilestyleu="wrap" tilestylev="wrap"/>
<m:texture2dgroup id="2" texid="1"><m:tex2coord u="0" v="0"/><m:tex2coord u="1" v="0.25"/><m:tex2coord u="1" v="1"/></m:texture2dgroup>
<basematerials id="3"><base name="red" displaycolor="#FF0000"/></basematerials>
<m:texture2d id="4" path="/3D/Texture/missing.png" contenttype="image/png"/>
<m:texture2dgroup id="5" texid="4"><m:tex2coord u="0" v="0"/></m:texture2dgroup>
<object id="6" name="Mixed" pid="2" pindex="0"><mesh>
<vertices><vertex x="0" y="0" z="0"/><vertex x="1" y="0" z="0"/><vertex x="1" y="1" z="0"/><vertex x="0" y="1" z="0"/><vertex x="2" y="0" z="0"/></vertices>
<triangles>
<triangle v1="0" v2="1" v3="2" p1="0" p2="1" p3="2"/>
<triangle v1="0" v2="2" v3="3" pid="3" p1="0"/>
<triangle v1="1" v2="4" v3="2" pid="5" p1="0"/>
<triangle v1="1" v2="4" v3="2" p1="7"/>
</triangles></mesh></object>
<object id="7" name="Default" pid="2" pindex="1"><mesh>
<vertices><vertex x="0" y="0" z="0"/><vertex x="1" y="0" z="0"/><vertex x="1" y="1" z="0"/></vertices>
<triangles><triangle v1="0" v2="1" v3="2"/></triangles>
</mesh></object>
</resources>
<build><item objectid="6"/><item objectid="7"/></build>
</model>`

func tmfTexturedPackage(t testing.TB) []byte {
	return make3MF(t, map[string]string{
		"_rels/.rels":         tmfRels,
		"3D/main.model":       tmfTextured,
		"3D/Texture/tex.png":  string(holesPNG(t)),
		"[Content_Types].xml": "",
	})
}

func Test3MFTextures(t *testing.T) {
	data := tmfTexturedPackage(t)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	m, err := load3MF(zr, "t")
	if err != nil {
		t.Fatal(err)
	}
	checkModel(t, m)
	if len(m.Warnings) != 1 || !strings.Contains(m.Warnings[0], "missing.png") {
		t.Errorf("warnings = %q", m.Warnings)
	}
	mixed, def := m.Roots[0].Children[0], m.Roots[0].Children[1]
	if mixed.Mesh != nil || len(mixed.Children) != 2 || mixed.Children[1].Name != "Mixed.1" {
		t.Fatalf("mixed: %+v", mixed)
	}
	tm, plain := mixed.Children[0].Mesh, mixed.Children[1].Mesh
	if tm.Texture == nil || tm.Texture.Name != "tex.png" || tm.Texture.Cutout || tm.TriangleCount() != 1 || !allWhite(tm) {
		t.Errorf("textured: %+v", tm)
	}
	want := map[[3]float32][2]float32{{0, 0, 0}: {0, 1}, {1, 0, 0}: {1, 0.75}, {1, 1, 0}: {1, 0}}
	got := uvsByPosition(t, tm)
	for p, uv := range want {
		if got[p] != uv {
			t.Errorf("UV at %v = %v, want %v", p, got[p], uv)
		}
	}
	// Red, then two triangles with the default colour: a missing texture
	// and a coordinate index out of range.
	if plain.Texture != nil || plain.TriangleCount() != 3 || [3]float32(plain.Colors[:3]) != [3]float32{1, 0, 0} {
		t.Errorf("plain: %+v", plain)
	}
	// The object's pindex applies to every corner; the texture is shared.
	if me := def.Mesh; me == nil || me.Texture != tm.Texture || len(uvsByPosition(t, me)) != 3 || me.UVs[0] != 1 || me.UVs[1] != 0.75 {
		t.Errorf("default: %+v", def.Mesh)
	}
}
