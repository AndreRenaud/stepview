package step

import (
	"math"
	"path/filepath"
	"testing"
)

func TestParseRecords(t *testing.T) {
	src := `ISO-10303-21;
HEADER;
FILE_NAME('demo /* not a comment */','2024',(''),(''),'','','');
ENDSEC;
DATA;
/* a comment; with a semicolon */
#1=CARTESIAN_POINT('it''s',(1.,-2.5E1,3));
#2=(LENGTH_UNIT()NAMED_UNIT(*)SI_UNIT(.MILLI.,.METRE.));
#3 = MEASURE('\X2\4F60597D\X0\', LENGTH_MEASURE(25.4), $, .T.);
#4=EMPTY(());
ENDSEC;
END-ISO-10303-21;
`
	f, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "demo /* not a comment */" {
		t.Errorf("name = %q", f.Name)
	}
	p := f.Get(1)
	if p == nil || p.Type != "CARTESIAN_POINT" {
		t.Fatalf("entity 1 = %+v", p)
	}
	if p.Arg(0).Str != "it's" {
		t.Errorf("string = %q", p.Arg(0).Str)
	}
	c := coords(p.Arg(1).AsList())
	if c != (Vec3{1, -25, 3}) {
		t.Errorf("coords = %v", c)
	}
	u := f.Get(2)
	if !u.Is("SI_UNIT") || !u.Is("LENGTH_UNIT") || u.Is("PLANE_ANGLE_UNIT") {
		t.Errorf("complex entity parts wrong: %+v", u.Parts)
	}
	if args, _ := u.PartArgs("SI_UNIT"); len(args) != 2 || args[0].Str != "MILLI" {
		t.Errorf("SI_UNIT args = %+v", args)
	}
	m := f.Get(3)
	if m.Arg(0).Str != "你好" {
		t.Errorf("unicode string = %q", m.Arg(0).Str)
	}
	if m.Arg(1).Kind != KindTyped || m.Arg(1).AsFloat() != 25.4 {
		t.Errorf("typed value = %+v", m.Arg(1))
	}
	if m.Arg(2).Kind != KindNull || !m.Arg(3).AsBool() {
		t.Errorf("null/bool = %+v %+v", m.Arg(2), m.Arg(3))
	}
	if l := f.Get(4).Arg(0); l.Kind != KindList || len(l.List) != 0 {
		t.Errorf("empty list = %+v", l)
	}
	if n := len(f.OfType("NAMED_UNIT")); n != 1 {
		t.Errorf("OfType(NAMED_UNIT) = %d", n)
	}
}

func TestCDTSquareWithHole(t *testing.T) {
	outer := []UV{{0, 0}, {10, 0}, {10, 10}, {0, 10}}
	hole := []UV{{3, 3}, {3, 7}, {7, 7}, {7, 3}}
	c := newCDT(UVBox{0, 10, 0, 10})
	var ids [][]int
	for _, loop := range [][]UV{outer, hole} {
		var l []int
		for _, p := range loop {
			l = append(l, c.addPoint(p.U, p.V))
		}
		ids = append(ids, l)
	}
	for _, l := range ids {
		for i := range l {
			if err := c.addConstraint(l[i], l[(i+1)%len(l)]); err != nil {
				t.Fatal(err)
			}
		}
	}
	area := 0.0
	in := c.inside()
	for ti, ok := range in {
		if !ok {
			continue
		}
		v := c.tris[ti].v
		ax, ay := c.point(v[0])
		bx, by := c.point(v[1])
		cx, cy := c.point(v[2])
		a := ((bx-ax)*(cy-ay) - (by-ay)*(cx-ax)) / 2
		if a <= 0 {
			t.Errorf("triangle %d not counter-clockwise (area %g)", ti, a)
		}
		area += a
	}
	if math.Abs(area-84) > 1e-9 {
		t.Errorf("area = %g, want 84", area)
	}
}

func loadTestdata(t *testing.T, name string) *Model {
	t.Helper()
	m, err := LoadFile(filepath.Join("..", "..", "testdata", name), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func checkMesh(t *testing.T, name string, m *Mesh) {
	t.Helper()
	nv := len(m.Positions) / 3
	if len(m.Normals) != len(m.Positions) || len(m.Colors) != len(m.Positions) {
		t.Fatalf("%s: attribute length mismatch", name)
	}
	for _, i := range m.Indices {
		if int(i) >= nv {
			t.Fatalf("%s: index %d out of range", name, i)
		}
	}
	for i := 0; i < nv; i++ {
		n := Vec3{float64(m.Normals[i*3]), float64(m.Normals[i*3+1]), float64(m.Normals[i*3+2])}
		if l := n.Len(); math.IsNaN(l) || math.Abs(l-1) > 1e-3 {
			t.Fatalf("%s: vertex %d normal length %g", name, i, l)
		}
		for k := 0; k < 3; k++ {
			if v := float64(m.Positions[i*3+k]); math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatalf("%s: vertex %d has invalid position", name, i)
			}
		}
	}
}

func walk(n *Node, f func(*Node)) {
	f(n)
	for _, c := range n.Children {
		walk(c, f)
	}
}

func TestLoadSamples(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "*.stp"))
	if err != nil || len(files) == 0 {
		t.Fatal("no test data")
	}
	for _, path := range files {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			m := loadTestdata(t, name)
			if m.Stats.FailedFaces != 0 {
				t.Errorf("%d faces failed: %v", m.Stats.FailedFaces, m.Warnings)
			}
			if m.Stats.Triangles == 0 {
				t.Fatal("no triangles")
			}
			for _, r := range m.Roots {
				walk(r, func(n *Node) {
					if n.Mesh != nil {
						checkMesh(t, n.Name, n.Mesh)
					}
				})
			}
		})
	}
}

func TestAssemblyStructure(t *testing.T) {
	m := loadTestdata(t, "as1_pe.stp")
	if len(m.Roots) != 1 || m.Roots[0].Name != "AS1_ASM" {
		t.Fatalf("roots = %v", m.Roots)
	}
	var names []string
	for _, c := range m.Roots[0].Children {
		names = append(names, c.Name)
	}
	want := []string{"PLATE", "L-BRACKET_ASM", "L-BRACKET_ASM", "ROD"}
	if len(names) != len(want) {
		t.Fatalf("children = %v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("child %d = %q, want %q", i, names[i], want[i])
		}
	}
	// The two bracket sub-assemblies share meshes but are placed apart.
	a, b := m.Roots[0].Children[1], m.Roots[0].Children[2]
	if a.Local.T.Dist(b.Local.T) < 1 {
		t.Errorf("bracket assemblies are not separated: %v %v", a.Local.T, b.Local.T)
	}
	if a.Children[0].Mesh != b.Children[0].Mesh {
		t.Error("expected shared bracket mesh")
	}
}

// TestWatertight checks that closed solids tessellate without cracks: every
// edge (by position) must be shared by exactly two triangles.
func TestWatertight(t *testing.T) {
	for _, name := range []string{"block.stp", "cylcub.stp"} {
		m := loadTestdata(t, name)
		for _, r := range m.Roots {
			walk(r, func(n *Node) {
				if n.Mesh == nil {
					return
				}
				type key [6]float32
				count := map[key]int{}
				p := func(i uint32) [3]float32 {
					return [3]float32{n.Mesh.Positions[i*3], n.Mesh.Positions[i*3+1], n.Mesh.Positions[i*3+2]}
				}
				edge := func(a, b [3]float32) key {
					if a[0] > b[0] || a[0] == b[0] && (a[1] > b[1] || a[1] == b[1] && a[2] > b[2]) {
						a, b = b, a
					}
					return key{a[0], a[1], a[2], b[0], b[1], b[2]}
				}
				idx := n.Mesh.Indices
				for i := 0; i+2 < len(idx); i += 3 {
					for k := 0; k < 3; k++ {
						count[edge(p(idx[i+k]), p(idx[i+(k+1)%3]))]++
					}
				}
				for e, c := range count {
					if c != 2 {
						t.Errorf("%s/%s: edge %v used %d times", name, n.Name, e, c)
						return
					}
				}
			})
		}
	}
}

func TestModelScale(t *testing.T) {
	// moon_buggy_asm uses millimetre units; guard against unit handling
	// blowing the model up or shrinking it.
	m := loadTestdata(t, "moon_buggy_asm.stp")
	box := EmptyBox()
	walk(m.Roots[0], func(n *Node) {
		if n.Mesh != nil {
			box.Union(n.Mesh.Bounds)
		}
	})
	if d := box.Diag(); d < 10 || d > 1e5 {
		t.Errorf("unexpected model size %g mm", d)
	}
}
