package meshload

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// chunk encodes a 3DS chunk; parts are raw bytes or values for
// binary.Write.
func chunk(id uint16, parts ...any) []byte {
	var body bytes.Buffer
	for _, p := range parts {
		switch p := p.(type) {
		case []byte:
			body.Write(p)
		case string:
			body.WriteString(p)
			body.WriteByte(0)
		default:
			binary.Write(&body, binary.LittleEndian, p)
		}
	}
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, id)
	binary.Write(&out, binary.LittleEndian, uint32(body.Len()+6))
	out.Write(body.Bytes())
	return out.Bytes()
}

// cubeObject is a unit cube whose faces k and k+1 form side k/2, with the
// given smoothing group for each side and the first two sides in material
// "Red".
func cubeObject(name string, smooth [6]uint32) []byte {
	verts := make([]float32, 0, 24)
	for i := range 8 {
		verts = append(verts, float32(i&1), float32(i>>1&1), float32(i>>2&1))
	}
	quads := [][4]uint16{{0, 2, 3, 1}, {4, 5, 7, 6}, {0, 1, 5, 4}, {2, 6, 7, 3}, {0, 4, 6, 2}, {1, 3, 7, 5}}
	var faces []uint16
	var sm []uint32
	for i, q := range quads {
		faces = append(faces, q[0], q[1], q[2], 0, q[0], q[2], q[3], 0)
		sm = append(sm, smooth[i], smooth[i])
	}
	return chunk(tdsNamedObj, name, chunk(tdsTriMesh,
		chunk(tdsVertices, uint16(8), verts),
		chunk(tdsFaces, uint16(12), faces,
			chunk(tdsFaceMat, "Red", uint16(4), []uint16{0, 1, 2, 3}),
			chunk(tdsSmooth, sm),
		),
	))
}

func node(id uint16, name string, parent int16, extra ...any) []byte {
	parts := []any{chunk(tdsNodeID, id), chunk(tdsNodeHeader, name, uint16(0), uint16(0), parent)}
	return chunk(tdsObjectNode, append(parts, extra...)...)
}

func sample3DS() []byte {
	return chunk(tdsMain,
		chunk(0x0002, uint32(3)),
		chunk(tdsEditor,
			// Objects may come before the materials they use.
			cubeObject("Smooth", [6]uint32{1, 1, 1, 1, 1, 1}),
			cubeObject("Hard", [6]uint32{1, 2, 4, 8, 16, 32}),
			chunk(tdsMaterial,
				chunk(tdsMatName, "Red"),
				chunk(tdsDiffuse, chunk(tdsLinColor24, []byte{0, 255, 0}), chunk(tdsColor24, []byte{255, 0, 0})),
			),
			chunk(tdsNamedObj, "Camera01", chunk(0x4700, make([]byte, 32))),
		),
		chunk(tdsKeyframer,
			node(0, "$$$DUMMY", -1, chunk(tdsInstance, "Group")),
			node(1, "Smooth", 0),
			node(2, "Hard", -1),
		),
	)
}

func Test3DS(t *testing.T) {
	m, err := loadTDS(sample3DS(), "t", files(nil))
	if err != nil {
		t.Fatal(err)
	}
	checkModel(t, m)
	root := m.Roots[0]
	if len(root.Children) != 2 {
		t.Fatalf("unexpected tree: %+v", root.Children)
	}
	group, hard := root.Children[0], root.Children[1]
	if group.Name != "Group" || group.Mesh != nil || len(group.Children) != 1 || group.Children[0].Name != "Smooth" {
		t.Fatalf("group: %+v", group)
	}
	if hard.Name != "Hard" || hard.Mesh == nil {
		t.Fatalf("hard: %+v", hard)
	}
	smooth := group.Children[0].Mesh
	// One smoothing group: every corner of the cube has a single normal
	// (vertices are still split by colour).
	normals := map[[3]float32]map[[3]float32]bool{}
	for i := 0; i < len(smooth.Positions); i += 3 {
		p, n := [3]float32(smooth.Positions[i:i+3]), [3]float32(smooth.Normals[i:i+3])
		if normals[p] == nil {
			normals[p] = map[[3]float32]bool{}
		}
		normals[p][n] = true
	}
	for p, ns := range normals {
		if len(ns) != 1 {
			t.Errorf("smooth cube corner %v has %d normals", p, len(ns))
		}
	}
	if len(normals) != 8 {
		t.Errorf("smooth cube has %d corners", len(normals))
	}
	// A group per side: flat sides, four corners each.
	if n := len(hard.Mesh.Positions) / 3; n != 24 {
		t.Errorf("hard cube has %d vertices, want 24", n)
	}
	red, other := 0, 0
	for i := 0; i < len(hard.Mesh.Colors); i += 3 {
		switch [3]float32(hard.Mesh.Colors[i : i+3]) {
		case [3]float32{1, 0, 0}:
			red++
		case defaultColour:
			other++
		}
	}
	if red != 8 || other != 16 {
		t.Errorf("hard cube colours: %d red, %d default", red, other)
	}
}

func Test3DSNoKeyframer(t *testing.T) {
	data := chunk(tdsMain, chunk(tdsEditor, cubeObject("Only", [6]uint32{})))
	m, err := loadTDS(data, "t", files(nil))
	if err != nil {
		t.Fatal(err)
	}
	checkModel(t, m)
	// A single object becomes the root's mesh; smoothing group 0 is flat.
	if me := m.Roots[0].Mesh; me == nil || len(me.Positions)/3 != 24 {
		t.Fatalf("unexpected model: %+v", m.Roots[0])
	}
	// The material is missing.
	if len(m.Warnings) != 1 {
		t.Errorf("warnings = %q", m.Warnings)
	}
	if _, err := loadTDS([]byte("not a 3ds file"), "t", files(nil)); err == nil {
		t.Error("loaded garbage")
	}
}

func Test3DSTransparency(t *testing.T) {
	// cubeObject's first two sides are in material "Red".
	for _, tc := range []struct {
		percent []byte
		want    float32 // opacity of those sides
	}{
		{chunk(tdsPercentI, int16(40)), 0.6},
		{chunk(tdsPercentF, float32(25)), 0.75},
		{chunk(tdsPercentF, float32(250)), 0},
		{chunk(tdsPercentI, int16(0)), 1},
		{chunk(tdsPercentI, int16(-20)), 1},
		{chunk(tdsPercentF, float32(math.NaN())), 1},
	} {
		data := chunk(tdsMain, chunk(tdsEditor,
			chunk(tdsMaterial, chunk(tdsMatName, "Red"), chunk(tdsTransp, tc.percent)),
			cubeObject("Cube", [6]uint32{1, 2, 4, 8, 16, 32}),
		))
		m, err := loadTDS(data, "t", files(nil))
		if err != nil {
			t.Fatal(err)
		}
		checkModel(t, m)
		me := m.Roots[0].Mesh
		if tc.want == 1 {
			if me.Alpha != nil {
				t.Errorf("%g: alphas %v", tc.want, me.Alpha)
			}
			continue
		}
		if got := alphaCounts(me); got[tc.want] != 8 || got[1] != 16 {
			t.Errorf("%g: alphas %v", tc.want, got)
		}
	}
}
