package step

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// minimalStep is a small but complete file used to seed the parser fuzzers.
const minimalStep = `ISO-10303-21;
HEADER;
FILE_NAME('demo /* not a comment */','2024',(''),(''),'','','');
ENDSEC;
DATA;
/* a comment; with a semicolon */
#1=CARTESIAN_POINT('it''s',(1.,-2.5E1,3));
#2=(LENGTH_UNIT()NAMED_UNIT(*)SI_UNIT(.MILLI.,.METRE.));
#3 = MEASURE('\X2\4F60597D\X0\', LENGTH_MEASURE(25.4), $, .T.);
#4=EMPTY(());
#5=B("0123",'\X\E9\S\a\P\b\\',#1,!USER(.F.));
ENDSEC;
END-ISO-10303-21;
`

// addTestdataSeeds adds the small sample files as seeds; the larger ones
// make each fuzz iteration too slow to be useful.
func addTestdataSeeds(f *testing.F) {
	for _, name := range []string{"block.stp", "cylcub.stp"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(minimalStep))
	f.Add([]byte("DATA;#1=A();ENDSEC;"))
	f.Add([]byte("DATA;#1=A(\"unterminated);"))
	f.Add([]byte("DATA;#99999999=A(#1);#1=B(#99999999);"))
	addTestdataSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		file, err := Parse(data)
		if err != nil {
			return
		}
		if file.Count() <= 0 {
			t.Fatalf("Parse succeeded with %d entities", file.Count())
		}
		for typ, ents := range file.byType {
			for _, e := range ents {
				if !e.Is(typ) {
					t.Fatalf("entity #%d listed under %s but Is(%q) is false", e.ID, typ, typ)
				}
				if _, ok := e.PartArgs(typ); !ok {
					t.Fatalf("entity #%d listed under %s but has no args for it", e.ID, typ)
				}
				if file.Get(e.ID) == nil {
					t.Fatalf("entity #%d not found by Get", e.ID)
				}
			}
		}
	})
}

func FuzzParseRecord(f *testing.F) {
	for _, s := range []string{
		"#1=CARTESIAN_POINT('it''s',(1.,-2.5E1,3))",
		"#2=(LENGTH_UNIT()NAMED_UNIT(*)SI_UNIT(.MILLI.,.METRE.))",
		"#3 = MEASURE('\\X2\\4F60597D\\X0\\', LENGTH_MEASURE(25.4), $, .T.)",
		"#4=EMPTY(())",
		"#5=B(\"0123\",#1,!USER(.F.),.5,-.5E-3)",
		"#6=(A(",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		p := parser{s: data, intern: map[string]string{}}
		e, err := p.parseRecord()
		if err != nil {
			return
		}
		if p.i > len(data) {
			t.Fatalf("parser position %d past end %d", p.i, len(data))
		}
		if e.Type != "" && len(e.Parts) != 0 {
			t.Fatalf("entity is both simple and complex: %+v", e)
		}
		var check func(v Value, depth int)
		check = func(v Value, depth int) {
			switch v.Kind {
			case KindNull, KindDerived, KindNumber, KindString, KindEnum, KindRef:
				if len(v.List) != 0 {
					t.Fatalf("scalar value has list elements: %+v", v)
				}
			case KindList, KindTyped:
				for _, c := range v.List {
					check(c, depth+1)
				}
			default:
				t.Fatalf("unknown value kind %d", v.Kind)
			}
		}
		for _, v := range e.Args {
			check(v, 0)
		}
		for _, part := range e.Parts {
			for _, v := range part.Args {
				check(v, 0)
			}
		}
	})
}

// FuzzParseNumber checks that parseNumber's integer fast path agrees with
// strconv.ParseFloat, which handles everything else.
func FuzzParseNumber(f *testing.F) {
	for _, s := range []string{"0", "-0", "+7", "123456789012345", "1234567890123456", "1.", "1.E-3", "-.5", "1e400", "-", "+", "1-2", "inf", "0x10"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		got, err := parseNumber(b)
		want, werr := strconv.ParseFloat(string(b), 64)
		if (err == nil) != (werr == nil) {
			t.Fatalf("parseNumber(%q) err = %v, strconv err = %v", b, err, werr)
		}
		if err == nil && got != want && !(math.IsNaN(got) && math.IsNaN(want)) {
			t.Fatalf("parseNumber(%q) = %v, want %v", b, got, want)
		}
	})
}

func FuzzDecodeString(f *testing.F) {
	for _, s := range []string{
		"plain", "it''s", "a\\\\b", "\\X2\\4F60597D\\X0\\", "\\X4\\0001F600\\X0\\",
		"\\X\\E9", "\\S\\a", "\\PA\\x", "\\X2\\4F6", "\\X4\\zz", "\\", "line\nbreak\r",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		got := decodeString(raw)
		if !strings.ContainsAny(string(raw), "\\'\n\r") && got != string(raw) {
			t.Fatalf("decodeString(%q) = %q, want input unchanged", raw, got)
		}
	})
}

// FuzzLoad runs the whole pipeline: parsing, product structure and
// tessellation.
func FuzzLoad(f *testing.F) {
	f.Add([]byte(minimalStep))
	addTestdataSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := Load(data, DefaultOptions())
		if err != nil {
			return
		}
		// Tessellation recovers from panics so one bad face does not take the
		// viewer down, but each one is still a bug.
		for _, w := range m.Warnings {
			if strings.Contains(w, "panic:") {
				t.Fatalf("recovered panic: %s", w)
			}
		}
		for _, root := range m.Roots {
			walk(root, func(n *Node) {
				if n.Mesh == nil {
					return
				}
				nv := len(n.Mesh.Positions) / 3
				if len(n.Mesh.Positions)%3 != 0 || len(n.Mesh.Normals) != len(n.Mesh.Positions) || len(n.Mesh.Colors) != len(n.Mesh.Positions) {
					t.Fatalf("%s: attribute length mismatch", n.Name)
				}
				if len(n.Mesh.Indices)%3 != 0 {
					t.Fatalf("%s: %d indices is not a whole number of triangles", n.Name, len(n.Mesh.Indices))
				}
				for _, i := range n.Mesh.Indices {
					if int(i) >= nv {
						t.Fatalf("%s: index %d out of range (%d vertices)", n.Name, i, nv)
					}
				}
				for _, s := range n.Mesh.FaceStarts {
					if int(s) > len(n.Mesh.Indices) {
						t.Fatalf("%s: face start %d past %d indices", n.Name, s, len(n.Mesh.Indices))
					}
				}
			})
		}
	})
}

// FuzzCDT inserts points on a coarse grid, which makes duplicate and
// collinear points (the hard cases) common, then constrains edges between
// them and checks the triangulation stays consistent.
func FuzzCDT(f *testing.F) {
	f.Add([]byte{0, 0, 15, 0, 15, 15, 0, 15}, []byte{0, 1, 1, 2, 2, 3, 3, 0})
	f.Add([]byte{0, 0, 4, 4, 8, 8, 12, 12, 0, 12}, []byte{0, 3, 1, 4})
	f.Add([]byte{0, 0, 10, 0, 10, 10, 0, 10, 3, 3, 3, 7, 7, 7, 7, 3}, []byte{0, 1, 1, 2, 2, 3, 3, 0, 4, 5, 5, 6, 6, 7, 7, 4})
	f.Fuzz(func(t *testing.T, pts, cons []byte) {
		if len(pts) > 512 || len(cons) > 512 {
			return
		}
		c := newCDT(UVBox{0, 15, 0, 15})
		var ids []int
		for i := 0; i+1 < len(pts); i += 2 {
			ids = append(ids, c.addPoint(float64(pts[i]%16), float64(pts[i+1]%16)))
		}
		if len(ids) == 0 {
			return
		}
		for i := 0; i+1 < len(cons); i += 2 {
			a, b := ids[int(cons[i])%len(ids)], ids[int(cons[i+1])%len(ids)]
			if a == b {
				continue
			}
			if err := c.addConstraint(a, b); err != nil {
				// Crossing constraints are rejected, and callers discard the
				// triangulation, so it need not be consistent afterwards.
				return
			}
		}
		area := 0.0
		for ti, tr := range c.tris {
			o := c.orient(tr.v[0], tr.v[1], tr.v[2])
			// Within cdtEps the triangulator treats points as collinear, so
			// rounding can leave a flat triangle slightly negative.
			if !(o > -cdtEps) {
				t.Fatalf("triangle %d %v is not counter-clockwise (orient %g)", ti, tr.v, o)
			}
			area += o / 2
			for i, u := range tr.n {
				if u < 0 {
					continue
				}
				back := false
				for _, w := range c.tris[u].n {
					back = back || w == ti
				}
				if !back {
					t.Fatalf("triangle %d neighbours %d (edge %d) but not vice versa", ti, u, i)
				}
			}
		}
		// The triangles tile the super triangle exactly.
		if want := 300.0 * 300 / 2; math.Abs(area-want) > 1e-6*want {
			t.Fatalf("total area = %g, want %g", area, want)
		}
		c.inside()
	})
}

// FuzzBSplineCurve builds curves from fuzzed control points, weights and
// non-decreasing knots, and checks evaluation stays inside the convex hull
// of the control points.
func FuzzBSplineCurve(f *testing.F) {
	f.Add(uint8(2), false, []byte{0, 0, 0, 1, 1, 1}, []byte{0, 0, 0, 0, 0, 0, 1, 0, 1, 0, 0, 0, 2, 0, 0, 0, 0, 0})
	f.Add(uint8(1), true, []byte{0, 0, 3, 4, 4}, []byte{0, 0, 0, 0, 0, 0, 64, 0, 1, 0, 1, 0, 1, 1, 128})
	f.Add(uint8(17), false, make([]byte, 40), make([]byte, 6*20))
	f.Fuzz(func(t *testing.T, deg uint8, rational bool, knotSteps, ctrlData []byte) {
		p := 1 + int(deg%20)
		stride := 6
		if rational {
			stride = 7
		}
		var ctrl []Vec3
		var w []float64
		lo, hi := Vec3{math.Inf(1), math.Inf(1), math.Inf(1)}, Vec3{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
		for i := 0; i+stride <= len(ctrlData) && len(ctrl) < 64; i += stride {
			v := Vec3{
				float64(int16(binary.LittleEndian.Uint16(ctrlData[i:]))),
				float64(int16(binary.LittleEndian.Uint16(ctrlData[i+2:]))),
				float64(int16(binary.LittleEndian.Uint16(ctrlData[i+4:]))),
			}
			ctrl = append(ctrl, v)
			lo = Vec3{math.Min(lo.X, v.X), math.Min(lo.Y, v.Y), math.Min(lo.Z, v.Z)}
			hi = Vec3{math.Max(hi.X, v.X), math.Max(hi.Y, v.Y), math.Max(hi.Z, v.Z)}
			if rational {
				w = append(w, 0.25+float64(ctrlData[i+6])/64)
			}
		}
		var knots []float64
		k := 0.0
		for _, s := range knotSteps {
			k += float64(s)
			knots = append(knots, k)
		}
		c, err := newBSplineCurve(p, ctrl, w, knots)
		if err != nil {
			return
		}
		t0, t1 := c.Range()
		tol := 1e-9 * (1 + hi.Sub(lo).Len())
		for i := -1; i <= 33; i++ {
			u := t0 + (t1-t0)*float64(i)/32
			pt, d := c.EvalD(u)
			if math.IsNaN(pt.Len()) || math.IsInf(pt.Len(), 0) || math.IsNaN(d.Len()) {
				t.Fatalf("EvalD(%g) = %v, %v", u, pt, d)
			}
			if pt.X < lo.X-tol || pt.Y < lo.Y-tol || pt.Z < lo.Z-tol || pt.X > hi.X+tol || pt.Y > hi.Y+tol || pt.Z > hi.Z+tol {
				t.Fatalf("Eval(%g) = %v outside control point bounds %v..%v", u, pt, lo, hi)
			}
			if pu := c.Project(pt); !(pu >= t0 && pu <= t1) {
				t.Fatalf("Project(%v) = %g outside [%g, %g]", pt, pu, t0, t1)
			}
		}
	})
}
