package gltfload

import (
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/modeler"

	"github.com/AndreRenaud/stepview/internal/step"
)

func TestLoadCube(t *testing.T) {
	doc := gltf.NewDocument()
	pos := [][3]float32{{0, 0, 0}, {1, 0, 0}, {1, 1, 0}, {0, 1, 0}}
	idx := []uint16{0, 1, 2, 0, 2, 3}
	attrs := gltf.PrimitiveAttributes{gltf.POSITION: modeler.WritePosition(doc, pos)}
	doc.Meshes = []*gltf.Mesh{{Name: "quad", Primitives: []*gltf.Primitive{{
		Indices:    new(modeler.WriteIndices(doc, idx)),
		Attributes: attrs,
	}}}}
	doc.Nodes = []*gltf.Node{{Name: "Quad", Mesh: new(0), Translation: [3]float64{0, 2, 0}}}
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

func TestTextures(t *testing.T) {
	fsys := fstest.MapFS{"tex file.png": {Data: testPNG(t, true)}}
	for _, tc := range []struct {
		where  string
		holes  bool
		mode   gltf.AlphaMode
		cutout bool
	}{
		{"view", true, gltf.AlphaOpaque, false},
		{"view", true, gltf.AlphaMask, true},
		{"data", false, gltf.AlphaMask, true},
		{"file", true, gltf.AlphaBlend, false},
	} {
		m, err := load(texturedDoc(t, tc.where, tc.holes, tc.mode), "t", fsys)
		if err != nil {
			t.Fatal(err)
		}
		me := m.Roots[0].Children[0].Children[0].Mesh
		if me.Texture == nil || me.Texture.Cutout != tc.cutout || len(m.Warnings) != 0 {
			t.Errorf("%s %v: texture %+v, warnings %q", tc.where, tc.mode, me.Texture, m.Warnings)
			continue
		}
		// TEXCOORD_1, unflipped; the base colour factor stays.
		if want := []float32{0, 0, 2, 0, 2, 1, 0, 1}; !slices.Equal(me.UVs, want) {
			t.Errorf("%s: UVs = %v", tc.where, me.UVs)
		}
		if c := me.Colors[:3]; c[0] < 0.99 || c[1] > 0.01 {
			t.Errorf("%s: colour = %v", tc.where, c)
		}
	}

	// Two primitives on one image with different alpha modes share its
	// pixels; primitives with the same mode share the texture.
	doc := texturedDoc(t, "view", true, gltf.AlphaOpaque)
	mask := *doc.Materials[0]
	mask.AlphaMode = gltf.AlphaMask
	doc.Materials = append(doc.Materials, &mask)
	p := *doc.Meshes[0].Primitives[0]
	doc.Meshes[0].Primitives = append(doc.Meshes[0].Primitives, &p, &gltf.Primitive{Attributes: p.Attributes, Indices: p.Indices, Material: new(1)})
	m, err := load(doc, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	var tex []*step.Texture
	for _, c := range m.Roots[0].Children[0].Children[0].Children {
		tex = append(tex, c.Mesh.Texture)
	}
	if len(tex) != 3 || tex[0] != tex[1] || tex[0] == tex[2] || tex[0].Image != tex[2].Image || tex[0].Cutout || !tex[2].Cutout {
		t.Errorf("textures = %+v", tex)
	}

	// A missing file or texture coordinate set leaves the mesh untextured.
	doc = texturedDoc(t, "file", false, gltf.AlphaOpaque)
	m, err = load(doc, "t", fstest.MapFS{})
	if err != nil {
		t.Fatal(err)
	}
	if me := m.Roots[0].Children[0].Children[0].Mesh; me.Texture != nil || me.UVs != nil || len(m.Warnings) != 1 {
		t.Errorf("missing file: %+v, warnings %q", me.Texture, m.Warnings)
	}
	doc = texturedDoc(t, "data", false, gltf.AlphaOpaque)
	doc.Materials[0].PBRMetallicRoughness.BaseColorTexture.TexCoord = 2
	m, err = load(doc, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	if me := m.Roots[0].Children[0].Children[0].Mesh; me.Texture != nil || me.UVs != nil || len(m.Warnings) != 1 {
		t.Errorf("missing coordinates: %+v, warnings %q", me.Texture, m.Warnings)
	}
}

func TestLoadSamples(t *testing.T) {
	var paths []string
	for _, pat := range []string{"*.glb", "*.gltf", "*/*.glb", "*/*.gltf"} {
		p, _ := filepath.Glob(filepath.Join("..", "..", "testdata", pat))
		paths = append(paths, p...)
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
			// Every sample is textured.
			textured := 0
			var walk func(n *step.Node)
			walk = func(n *step.Node) {
				if me := n.Mesh; me != nil && me.Texture != nil {
					if len(me.UVs) != len(me.Positions)/3*2 {
						t.Fatalf("%s: %d UVs for %d vertices", n.Name, len(me.UVs), len(me.Positions)/3)
					}
					textured++
				}
				for _, c := range n.Children {
					walk(c)
				}
			}
			for _, r := range m.Roots {
				walk(r)
			}
			if textured == 0 || len(m.Warnings) != 0 {
				t.Errorf("%d textured meshes, warnings %q", textured, m.Warnings)
			}
		})
	}
}
