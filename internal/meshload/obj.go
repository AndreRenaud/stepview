package meshload

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/AndreRenaud/stepview/internal/step"
)

type objGroup struct {
	name string
	b    builder
}

// objMaterial is an MTL material's diffuse colour, opacity and texture.
type objMaterial struct {
	colour        [3]float32
	alpha         float32    // d, or 1 - Tr
	tex, mask     string     // map_Kd and map_d file names
	scale, offset [2]float32 // map_Kd -s and -o
}

func newOBJMaterial() *objMaterial {
	return &objMaterial{colour: defaultColour, alpha: 1, scale: [2]float32{1, 1}}
}

// loadOBJ reads a Wavefront OBJ file and the diffuse colours, opacities and
// textures from its MTL material libraries. OBJ has no units and is conventionally
// Y up, so the model is turned to Z up and its coordinates are used as
// millimetres. open reads a material library or texture named in it.
func loadOBJ(data []byte, name string, open func(string) ([]byte, error)) (*step.Model, error) {
	var (
		pos      [][3]float32
		vcol     [][3]float32 // per vertex, valid where hasCol is set
		hasCol   []bool
		nrm      [][3]float32
		uvs      [][2]float32
		mats     = map[string]*objMaterial{}
		missing  = map[string]bool{}
		mat      = newOBJMaterial()
		tex      *step.Texture
		groups   []*objGroup
		byName   = map[string]*objGroup{}
		object   string
		group    string
		cur      *objGroup
		warnings []string
		badFaces int
	)
	tx := newTextures(open, func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	})
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
	var fuv [][2]float32
	var fa []float32
	var fv []int
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
		case "vt":
			var v [2]float32
			floats(f[1:], v[:])
			uvs = append(uvs, v)
		case "vn":
			var v [3]float32
			if floats(f[1:], v[:]) == 3 {
				nrm = append(nrm, v)
			} else {
				nrm = append(nrm, [3]float32{})
			}
		case "f":
			fp, fc, fn, fuv, fa, fv = fp[:0], fc[:0], fn[:0], fuv[:0], fa[:0], fv[:0]
			ok, allNrm, allUV := len(f) >= 4, true, tex != nil
			for _, c := range f[1:] {
				parts := strings.Split(c, "/")
				vi, good := index(parts[0], len(pos))
				if !good {
					ok = false
					break
				}
				fp = append(fp, pos[vi])
				fv = append(fv, vi)
				ti, good := -1, false
				if len(parts) >= 2 && allUV {
					ti, good = index(parts[1], len(uvs))
				}
				if good {
					// Bottom left origin -> top left.
					uv := uvs[ti]
					fuv = append(fuv, [2]float32{uv[0]*mat.scale[0] + mat.offset[0], 1 - uv[1]*mat.scale[1] - mat.offset[1]})
				} else {
					allUV = false
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
			// A texture replaces the material colour, which exporters often
			// leave black or grey.
			base, ftex, uv, n := mat.colour, tex, fuv, fn
			if allUV {
				base = [3]float32{1, 1, 1}
			} else {
				ftex, uv = nil, nil
			}
			for _, vi := range fv {
				if hasCol[vi] {
					fc = append(fc, vcol[vi])
				} else {
					fc = append(fc, base)
				}
				fa = append(fa, mat.alpha)
			}
			if !allNrm {
				n = nil
			}
			cur.b.addPolygon(fp, fc, n, ftex, uv, fa)
		case "o":
			object, group = strings.Join(f[1:], " "), ""
			selectGroup()
		case "g":
			group = strings.Join(f[1:], " ")
			selectGroup()
		case "usemtl":
			mn := strings.Join(f[1:], " ")
			m, ok := mats[mn]
			if !ok {
				m = newOBJMaterial()
				if !missing[mn] {
					missing[mn] = true
					warnings = append(warnings, fmt.Sprintf("material %q not found", mn))
				}
			}
			mat, tex = m, tx.load(m.tex, m.mask)
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
	var meshes [][]*step.Mesh
	for i, g := range groups {
		if g.b.dropped > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: %d triangles with invalid coordinates", g.name, g.b.dropped))
		}
		ms := g.b.meshes()
		if len(ms) == 0 {
			continue
		}
		gn := g.name
		if gn == "" {
			gn = fmt.Sprintf("Group %d", i+1)
		}
		nodes = append(nodes, &step.Node{Name: gn, Local: step.Identity()})
		meshes = append(meshes, ms)
	}
	switch len(nodes) {
	case 0:
		return nil, errors.New("obj: no faces found")
	case 1:
		setMeshes(root, meshes[0])
	default:
		for i, n := range nodes {
			setMeshes(n, meshes[i])
		}
		root.Children = nodes
	}
	return finish(name, []*step.Node{root}, warnings), nil
}

// readMTL adds the diffuse colours (Kd), opacities (d or Tr) and textures
// (map_Kd, with map_d as its alpha) of a material library to mats.
func readMTL(data []byte, mats map[string]*objMaterial) {
	cur := &objMaterial{}
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
			cur = newOBJMaterial()
			mats[strings.Join(f[1:], " ")] = cur
		case "map_Kd":
			cur.tex, cur.scale, cur.offset = parseMap(f[1:])
		case "map_d":
			cur.mask, _, _ = parseMap(f[1:])
		case "d", "Tr":
			// "d -halo" fades with the viewing angle, which is ignored.
			g := f[1:]
			if len(g) > 0 && g[0] == "-halo" {
				g = g[1:]
			}
			if len(g) == 0 {
				break
			}
			if v, err := strconv.ParseFloat(g[0], 32); err == nil {
				if f[0] == "Tr" {
					v = 1 - v
				}
				cur.alpha = float32(v)
			}
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
				cur.colour = [3]float32{c[0], c[0], c[0]}
			case 3:
				cur.colour = c
			}
		}
	}
}

// mapArgs is the number of arguments each MTL texture map option takes;
// -o, -s and -t take one to three numbers.
var mapArgs = map[string]int{
	"-blendu": 1, "-blendv": 1, "-bm": 1, "-boost": 1, "-cc": 1, "-clamp": 1,
	"-imfchan": 1, "-texres": 1, "-type": 1, "-mm": 2, "-o": 3, "-s": 3, "-t": 3,
}

// parseMap reads the arguments of a texture map statement: options, then
// a file name that may contain spaces. It returns the name and the
// texture coordinate scale (-s) and offset (-o).
func parseMap(f []string) (name string, scale, offset [2]float32) {
	scale = [2]float32{1, 1}
	for len(f) > 1 {
		n, ok := mapArgs[f[0]]
		if !ok {
			break
		}
		opt := f[0]
		f = f[1:]
		if opt != "-o" && opt != "-s" && opt != "-t" {
			f = f[min(n, len(f)-1):]
			continue
		}
		var v []float32
		for len(v) < n && len(f) > 1 {
			x, err := strconv.ParseFloat(f[0], 32)
			if err != nil {
				break
			}
			v = append(v, float32(x))
			f = f[1:]
		}
		switch {
		case opt == "-s" && len(v) > 0:
			scale = [2]float32{v[0], 1}
			if len(v) > 1 {
				scale[1] = v[1]
			}
		case opt == "-o" && len(v) > 0:
			offset = [2]float32{v[0], 0}
			if len(v) > 1 {
				offset[1] = v[1]
			}
		}
	}
	return strings.Join(f, " "), scale, offset
}
