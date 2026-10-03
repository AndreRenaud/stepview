package meshload

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/AndreRenaud/stepview/internal/step"
)

// LoadSTL reads a binary or ASCII STL file. STL has no units; like most
// CAD and 3D printing software, it is taken to be millimetres with Z up.
func LoadSTL(path string) (*step.Model, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return loadSTL(data, baseName(path))
}

func loadSTL(data []byte, name string) (*step.Model, error) {
	var parts []stlPart
	var err error
	if isBinarySTL(data) {
		parts, err = readBinarySTL(data)
	} else {
		parts, err = readASCIISTL(data)
	}
	if err != nil {
		return nil, err
	}
	root := &step.Node{Name: name, Local: step.Identity()}
	var warnings []string
	for i, p := range parts {
		if p.b.dropped > 0 {
			warnings = append(warnings, fmt.Sprintf("solid %d: %d triangles with invalid coordinates", i, p.b.dropped))
		}
		m := p.b.mesh()
		if m == nil {
			continue
		}
		if len(parts) == 1 {
			root.Mesh = m
			if p.name != "" {
				root.Name = p.name
			}
			break
		}
		pn := p.name
		if pn == "" {
			pn = fmt.Sprintf("Solid %d", i+1)
		}
		root.Children = append(root.Children, &step.Node{Name: pn, Local: step.Identity(), Mesh: m})
	}
	if root.Mesh == nil && len(root.Children) == 0 {
		return nil, errors.New("stl: no triangles found")
	}
	return finish(name, []*step.Node{root}, warnings), nil
}

type stlPart struct {
	name string
	b    builder
}

// isBinarySTL reports whether data is a binary STL. Some exporters start
// the binary header with "solid" too, so the size is the main test.
func isBinarySTL(data []byte) bool {
	if len(data) < 84 {
		return false
	}
	n := uint64(binary.LittleEndian.Uint32(data[80:84]))
	if uint64(len(data)) == 84+50*n {
		return true
	}
	head := bytes.TrimLeft(data[:min(len(data), 512)], " \t\r\n")
	if !bytes.HasPrefix(head, []byte("solid")) {
		return true
	}
	// "solid" followed by binary data rather than text.
	return bytes.IndexByte(head, 0) >= 0
}

func readBinarySTL(data []byte) ([]stlPart, error) {
	if len(data) < 84 {
		return nil, errors.New("stl: file too short")
	}
	n := int(binary.LittleEndian.Uint32(data[80:84]))
	if avail := (len(data) - 84) / 50; n > avail {
		n = avail
	}
	// Materialise Magics writes "COLOR=rgba" in the header for the default
	// colour and uses bit 15 clear for per-face colours (red in the low
	// bits); VisCAM/SolidView use bit 15 set (blue in the low bits).
	magics := false
	base := defaultColour
	if i := bytes.Index(data[:80], []byte("COLOR=")); i >= 0 && i+10 <= 80 {
		magics = true
		c := data[i+6 : i+9]
		base = [3]float32{float32(c[0]) / 255, float32(c[1]) / 255, float32(c[2]) / 255}
	}
	f32 := func(b []byte) float32 { return math.Float32frombits(binary.LittleEndian.Uint32(b)) }
	var part stlPart
	part.b.pos = make([][3]float32, 0, n*3)
	part.b.col = make([][3]float32, 0, n*3)
	part.b.nrm = make([][3]float32, 0, n*3)
	part.b.hasNrm = make([]bool, 0, n)
	for i := range n {
		r := data[84+i*50 : 84+(i+1)*50]
		var p [3][3]float32
		for k := range 3 {
			o := 12 + k*12
			p[k] = [3]float32{f32(r[o:]), f32(r[o+4:]), f32(r[o+8:])}
		}
		c := base
		attr := binary.LittleEndian.Uint16(r[48:])
		five := func(shift uint) float32 { return float32(attr>>shift&31) / 31 }
		switch {
		case magics && attr&0x8000 == 0:
			c = [3]float32{five(0), five(5), five(10)}
		case !magics && attr&0x8000 != 0:
			c = [3]float32{five(10), five(5), five(0)}
		}
		part.b.add(p, [3][3]float32{c, c, c}, nil)
	}
	return []stlPart{part}, nil
}

// readASCIISTL reads one or more "solid ... endsolid" blocks. It is lenient:
// the vertices of each facet's loop are fan triangulated and the facet
// normals (often wrong in practice) are ignored.
func readASCIISTL(data []byte) ([]stlPart, error) {
	var parts []stlPart
	var loop, cols [][3]float32
	flush := func() {
		if len(parts) > 0 && len(loop) >= 3 {
			for len(cols) < len(loop) {
				cols = append(cols, defaultColour)
			}
			parts[len(parts)-1].b.addPolygon(loop, cols[:len(loop)], nil, nil, nil, nil)
		}
		loop = loop[:0]
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		switch strings.ToLower(f[0]) {
		case "solid":
			flush()
			parts = append(parts, stlPart{name: strings.Join(f[1:], " ")})
		case "vertex":
			if len(f) < 4 {
				return nil, fmt.Errorf("stl: line %d: vertex needs three coordinates", line)
			}
			var v [3]float32
			for k := range 3 {
				x, err := strconv.ParseFloat(f[k+1], 32)
				if err != nil {
					return nil, fmt.Errorf("stl: line %d: %v", line, err)
				}
				v[k] = float32(x)
			}
			if len(parts) == 0 {
				parts = append(parts, stlPart{})
			}
			loop = append(loop, v)
		case "endloop", "endfacet", "endsolid":
			flush()
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("stl: %v", err)
	}
	flush()
	return parts, nil
}
