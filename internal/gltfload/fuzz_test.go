package gltfload

import (
	"bytes"
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
	f.Fuzz(func(t *testing.T, data []byte) {
		var doc gltf.Document
		// An empty file system keeps external buffer URIs off the real disk.
		if err := gltf.NewDecoderFS(bytes.NewReader(data), fstest.MapFS{}).Decode(&doc); err != nil {
			return
		}
		m, err := load(&doc, "fuzz")
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
