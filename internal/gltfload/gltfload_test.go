package gltfload

import (
	"path/filepath"
	"testing"

	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/modeler"
)

func TestLoadCube(t *testing.T) {
	doc := gltf.NewDocument()
	pos := [][3]float32{{0, 0, 0}, {1, 0, 0}, {1, 1, 0}, {0, 1, 0}}
	idx := []uint16{0, 1, 2, 0, 2, 3}
	attrs := gltf.PrimitiveAttributes{gltf.POSITION: modeler.WritePosition(doc, pos)}
	doc.Meshes = []*gltf.Mesh{{Name: "quad", Primitives: []*gltf.Primitive{{
		Indices:    gltf.Index(modeler.WriteIndices(doc, idx)),
		Attributes: attrs,
	}}}}
	doc.Nodes = []*gltf.Node{{Name: "Quad", Mesh: gltf.Index(0), Translation: [3]float64{0, 2, 0}}}
	doc.Scenes[0].Nodes = append(doc.Scenes[0].Nodes, 0)
	path := filepath.Join(t.TempDir(), "quad.glb")
	if err := gltf.SaveBinary(doc, path); err != nil {
		t.Fatal(err)
	}
	m, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Stats.Triangles != 2 {
		t.Fatalf("triangles = %d", m.Stats.Triangles)
	}
	root := m.Roots[0]
	if len(root.Children) != 1 || root.Children[0].Name != "Quad" || root.Children[0].Mesh == nil {
		t.Fatalf("unexpected tree: %+v", root.Children)
	}
	// glTF Y up (metres) -> Z up (millimetres): the node's +2 m in Y lands
	// at +2000 mm in Z.
	p := root.Local.Mul(root.Children[0].Local).Apply(root.Children[0].Mesh.Bounds.Min)
	if p.Z < 1999 || p.Z > 2001 {
		t.Errorf("converted position = %v", p)
	}
	if n := root.Children[0].Mesh.Normals; n[2] < 0.99 && n[2] > -0.99 {
		t.Errorf("computed normal = %v", n[:3])
	}
}
