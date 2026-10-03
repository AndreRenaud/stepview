package gltfload

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"math"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/modeler"

	"github.com/AndreRenaud/stepview/internal/step"
)

// quadDoc builds a small document with indices, normals, colours and a
// material so the seeds reach every attribute path.
func quadDoc() *gltf.Document {
	doc := gltf.NewDocument()
	pos := [][3]float32{{0, 0, 0}, {1, 0, 0}, {1, 1, 0}, {0, 1, 0}}
	nrm := [][3]float32{{0, 0, 1}, {0, 0, 1}, {0, 0, 1}, {0, 0, 1}}
	col := [][4]uint8{{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255}, {255, 255, 255, 255}}
	attrs := gltf.PrimitiveAttributes{
		gltf.POSITION: modeler.WritePosition(doc, pos),
		gltf.NORMAL:   modeler.WriteNormal(doc, nrm),
		gltf.COLOR_0:  modeler.WriteColor(doc, col),
	}
	doc.Materials = []*gltf.Material{{PBRMetallicRoughness: &gltf.PBRMetallicRoughness{BaseColorFactor: &[4]float64{1, 0.5, 0.25, 1}}}}
	doc.Meshes = []*gltf.Mesh{{Name: "quad", Primitives: []*gltf.Primitive{{
		Indices:    new(modeler.WriteIndices(doc, []uint16{0, 1, 2, 0, 2, 3})),
		Attributes: attrs,
		Material:   new(0),
	}}}}
	doc.Nodes = []*gltf.Node{
		{Name: "Parent", Children: []int{1}, Scale: [3]float64{2, 2, 2}},
		{Name: "Quad", Mesh: new(0), Translation: [3]float64{0, 2, 0}},
	}
	doc.Scenes[0].Nodes = append(doc.Scenes[0].Nodes, 0)
	return doc
}

// testPNG is a 4x4 image, transparent on the left when holes is set.
func testPNG(t testing.TB, holes bool) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := range 4 {
		for x := range 4 {
			c := color.NRGBA{uint8(x * 60), uint8(y * 60), 255, 255}
			if holes && x < 2 {
				c.A = 0
			}
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// texturedDoc is quadDoc with a base colour texture using the second
// texture coordinate set. The image is stored as where says: in a buffer
// "view", a "data" URI, or a "file" named "tex file.png".
func texturedDoc(t testing.TB, where string, holes bool, mode gltf.AlphaMode) *gltf.Document {
	doc := quadDoc()
	data := testPNG(t, holes)
	switch where {
	case "view":
		if _, err := modeler.WriteImage(doc, "tex", "image/png", bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
	case "data":
		doc.Images = append(doc.Images, &gltf.Image{URI: "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)})
	case "file":
		doc.Images = append(doc.Images, &gltf.Image{URI: "tex%20file.png"})
	}
	doc.Textures = []*gltf.Texture{{Source: new(0)}}
	p := doc.Meshes[0].Primitives[0]
	p.Attributes[gltf.TEXCOORD_0] = modeler.WriteTextureCoord(doc, [][2]float32{{9, 9}, {9, 9}, {9, 9}, {9, 9}})
	p.Attributes[gltf.TEXCOORD_1] = modeler.WriteTextureCoord(doc, [][2]float32{{0, 0}, {2, 0}, {2, 1}, {0, 1}})
	mat := doc.Materials[0]
	mat.PBRMetallicRoughness.BaseColorTexture = &gltf.TextureInfo{Index: 0, TexCoord: 1}
	mat.AlphaMode = mode
	return doc
}

func encode(t testing.TB, doc *gltf.Document, binary bool) []byte {
	var buf bytes.Buffer
	enc := gltf.NewEncoder(&buf)
	enc.AsBinary = binary
	if err := enc.Encode(doc); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func FuzzLoad(f *testing.F) {
	f.Add(encode(f, quadDoc(), true))
	f.Add(encode(f, quadDoc(), false))
	f.Add([]byte(`{"asset":{"version":"2.0"},"nodes":[{"mesh":0}],"meshes":[{"primitives":[{"attributes":{"POSITION":5}}]}]}`))
	f.Add([]byte(`{"asset":{"version":"2.0"},"scene":3,"nodes":[{"children":[0]}]}`))
	f.Add(encode(f, texturedDoc(f, "view", true, gltf.AlphaMask), true))
	f.Add(encode(f, texturedDoc(f, "data", false, gltf.AlphaOpaque), false))
	f.Add(encode(f, texturedDoc(f, "file", true, gltf.AlphaBlend), false))
	blend := quadDoc()
	blend.Materials[0].AlphaMode = gltf.AlphaBlend
	blend.Materials[0].PBRMetallicRoughness.BaseColorFactor[3] = 0.5
	f.Add(encode(f, blend, true))
	// External files come from memory, never the real disk.
	fsys := fstest.MapFS{"tex file.png": {Data: testPNG(f, true)}}
	f.Fuzz(func(t *testing.T, data []byte) {
		var doc gltf.Document
		if err := gltf.NewDecoderFS(bytes.NewReader(data), fsys).Decode(&doc); err != nil {
			return
		}
		m, err := load(&doc, "fuzz", fsys)
		if err != nil {
			return
		}
		triangles := 0
		var walk func(n *step.Node)
		walk = func(n *step.Node) {
			if n.Mesh != nil {
				nv := len(n.Mesh.Positions) / 3
				if len(n.Mesh.Normals) != len(n.Mesh.Positions) || len(n.Mesh.Colors) != len(n.Mesh.Positions) {
					t.Fatalf("%s: attribute length mismatch", n.Name)
				}
				for _, i := range n.Mesh.Indices {
					if int(i) >= nv {
						t.Fatalf("%s: index %d out of range (%d vertices)", n.Name, i, nv)
					}
				}
				if want := nv * 2; n.Mesh.Texture == nil && len(n.Mesh.UVs) != 0 || n.Mesh.Texture != nil && len(n.Mesh.UVs) != want {
					t.Fatalf("%s: textured %v with %d UVs for %d vertices", n.Name, n.Mesh.Texture != nil, len(n.Mesh.UVs), nv)
				}
				for _, v := range n.Mesh.UVs {
					if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
						t.Fatalf("%s: invalid UV %g", n.Name, v)
					}
				}
				if a := n.Mesh.Alpha; a != nil && (len(a) != nv || !slices.ContainsFunc(a, func(v float32) bool { return v < 1 })) {
					t.Fatalf("%s: %d alphas for %d vertices, %v", n.Name, len(a), nv, a)
				}
				for _, v := range n.Mesh.Alpha {
					if !(v >= 0 && v <= 1) {
						t.Fatalf("%s: invalid alpha %g", n.Name, v)
					}
				}
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
	})
}
