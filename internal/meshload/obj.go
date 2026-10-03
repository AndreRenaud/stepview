package meshload

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/AndreRenaud/stepview/internal/step"
)

// LoadOBJ reads a Wavefront OBJ file and the diffuse colours from its MTL
// material libraries. OBJ has no units and is conventionally Y up, so the
// model is turned to Z up and its coordinates are used as millimetres.
func LoadOBJ(path string) (*step.Model, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	return loadOBJ(data, baseName(path), func(name string) ([]byte, error) {
		return os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	})
}

type objGroup struct {
	name string
	b    builder
}

// loadOBJ parses an OBJ file; open reads a material library named in it.
func loadOBJ(data []byte, name string, open func(string) ([]byte, error)) (*step.Model, error) {
	var (
		pos      [][3]float32
		vcol     [][3]float32 // per vertex, valid where hasCol is set
		hasCol   []bool
		nrm      [][3]float32
		mats     = map[string][3]float32{}
		missing  = map[string]bool{}
		colour   = defaultColour
		groups   []*objGroup
		byName   = map[string]*objGroup{}
		object   string
		group    string
		cur      *objGroup
		warnings []string
		badFaces int
	)
	selectGroup := func() {
		label := object
		if group != "" && group != object {
			if label != "" {
				label += " / "
			}
			label += group
		}
		g, ok := byName[label]
		if !ok {
			g = &objGroup{name: label}
			byName[label] = g
			groups = append(groups, g)
		}
		cur = g
	}
	// index resolves a 1-based or negative (relative) OBJ index.
	index := func(s string, n int) (int, bool) {
		i, err := strconv.Atoi(s)
		if err != nil {
			return 0, false
		}
		if i < 0 {
			i += n
		} else {
			i--
		}
		return i, i >= 0 && i < n
	}
	floats := func(f []string, out []float32) int {
		k := 0
		for ; k < len(f) && k < len(out); k++ {
			v, err := strconv.ParseFloat(f[k], 32)
			if err != nil {
				break
			}
			out[k] = float32(v)
		}
		return k
	}

	var fp, fc, fn [][3]float32
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var pending string
	for sc.Scan() {
		line := sc.Text()
		if before, ok := strings.CutSuffix(line, "\\"); ok {
			pending += before + " "
			continue
		}
		line, pending = pending+line, ""
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "v":
			var v [6]float32
			n := floats(f[1:], v[:])
			if n < 3 {
				return nil, fmt.Errorf("obj: bad vertex %q", line)
			}
			pos = append(pos, [3]float32{v[0], v[1], v[2]})
			// "v x y z r g b" is a common extension for vertex colours.
			hasCol = append(hasCol, n == 6)
			vcol = append(vcol, [3]float32{v[3], v[4], v[5]})
		case "vn":
			var v [3]float32
			if floats(f[1:], v[:]) == 3 {
				nrm = append(nrm, v)
			} else {
				nrm = append(nrm, [3]float32{})
			}
		case "f":
			fp, fc, fn = fp[:0], fc[:0], fn[:0]
			ok, allNrm := len(f) >= 4, true
			for _, c := range f[1:] {
				parts := strings.Split(c, "/")
				vi, good := index(parts[0], len(pos))
				if !good {
					ok = false
					break
				}
				fp = append(fp, pos[vi])
				if hasCol[vi] {
					fc = append(fc, vcol[vi])
				} else {
					fc = append(fc, colour)
				}
				ni, good := -1, false
				if len(parts) == 3 {
					ni, good = index(parts[2], len(nrm))
				}
				if good {
					fn = append(fn, nrm[ni])
				} else {
					allNrm = false
				}
			}
			if !ok {
				badFaces++
				continue
			}
			if cur == nil {
				selectGroup()
			}
			if allNrm {
				cur.b.addPolygon(fp, fc, fn)
			} else {
				cur.b.addPolygon(fp, fc, nil)
			}
		case "o":
			object, group = strings.Join(f[1:], " "), ""
			selectGroup()
		case "g":
			group = strings.Join(f[1:], " ")
			selectGroup()
		case "usemtl":
			mat := strings.Join(f[1:], " ")
			c, ok := mats[mat]
			if !ok {
				c = defaultColour
				if !missing[mat] {
					missing[mat] = true
					warnings = append(warnings, fmt.Sprintf("material %q not found", mat))
				}
			}
			colour = c
		case "mtllib":
			// Names are usually space separated, but a single name may
			// itself contain spaces.
			libs := f[1:]
			if len(libs) > 1 {
				if _, err := open(libs[0]); err != nil {
					libs = []string{strings.Join(libs, " ")}
				}
			}
			for _, lib := range libs {
				data, err := open(lib)
				if err != nil {
					warnings = append(warnings, fmt.Sprintf("material library %s: %v", lib, err))
					continue
				}
				readMTL(data, mats)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("obj: %v", err)
	}
	if badFaces > 0 {
		warnings = append(warnings, fmt.Sprintf("%d faces with missing vertices", badFaces))
	}

	// Y up -> Z up.
	root := &step.Node{Name: name, Local: step.Affine{R: [3][3]float64{{1, 0, 0}, {0, 0, -1}, {0, 1, 0}}}}
	var nodes []*step.Node
	for i, g := range groups {
		if g.b.dropped > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: %d triangles with invalid coordinates", g.name, g.b.dropped))
		}
		m := g.b.mesh()
		if m == nil {
			continue
		}
		gn := g.name
		if gn == "" {
			gn = fmt.Sprintf("Group %d", i+1)
		}
		nodes = append(nodes, &step.Node{Name: gn, Local: step.Identity(), Mesh: m})
	}
	switch len(nodes) {
	case 0:
		return nil, errors.New("obj: no faces found")
	case 1:
		root.Mesh = nodes[0].Mesh
	default:
		root.Children = nodes
	}
	return finish(name, []*step.Node{root}, warnings), nil
}

// readMTL adds the diffuse colours (Kd) of a material library to mats.
func readMTL(data []byte, mats map[string][3]float32) {
	var cur string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "newmtl":
			cur = strings.Join(f[1:], " ")
			mats[cur] = defaultColour
		case "Kd":
			var c [3]float32
			n := 0
			for ; n < 3 && n+1 < len(f); n++ {
				v, err := strconv.ParseFloat(f[n+1], 32)
				if err != nil {
					break
				}
				c[n] = min(max(float32(v), 0), 1)
			}
			switch n {
			case 1: // "Kd r" means grey
				mats[cur] = [3]float32{c[0], c[0], c[0]}
			case 3:
				mats[cur] = c
			}
		}
	}
}
