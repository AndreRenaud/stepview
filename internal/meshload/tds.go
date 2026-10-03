package meshload

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/AndreRenaud/stepview/internal/step"
)

// LoadTDS reads an Autodesk 3D Studio (.3ds) file: its triangle meshes,
// material diffuse colours, transparency and textures, smoothing groups, and the object
// hierarchy from the keyframer. 3DS has no units; it is Z up and taken as
// millimetres. Mesh vertices are stored in world space, so the hierarchy
// only groups objects and every node has an identity transform.
func LoadTDS(path string) (*step.Model, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return loadTDS(data, baseName(path), dirOpener(filepath.Dir(path)))
}

// 3DS chunk identifiers.
const (
	tdsMain       = 0x4D4D
	tdsEditor     = 0x3D3D
	tdsNamedObj   = 0x4000
	tdsTriMesh    = 0x4100
	tdsVertices   = 0x4110
	tdsFaces      = 0x4120
	tdsFaceMat    = 0x4130
	tdsUV         = 0x4140
	tdsSmooth     = 0x4150
	tdsMaterial   = 0xAFFF
	tdsMatName    = 0xA000
	tdsDiffuse    = 0xA020
	tdsTransp     = 0xA050
	tdsTexMap     = 0xA200
	tdsOpacMap    = 0xA210
	tdsMapFile    = 0xA300
	tdsMapUScale  = 0xA354
	tdsMapVScale  = 0xA356
	tdsMapUOffset = 0xA358
	tdsMapVOffset = 0xA35A
	tdsColorF     = 0x0010
	tdsColor24    = 0x0011
	tdsLinColor24 = 0x0012
	tdsLinColorF  = 0x0013
	tdsPercentI   = 0x0030
	tdsPercentF   = 0x0031
	tdsKeyframer  = 0xB000
	tdsObjectNode = 0xB002
	tdsLastNode   = 0xB007
	tdsNodeHeader = 0xB010
	tdsInstance   = 0xB011
	tdsNodeID     = 0xB030
)

type tdsMatGroup struct {
	material string
	faces    []uint16
}

// tdsMat is a material's diffuse colour, opacity and texture.
type tdsMat struct {
	colour        [3]float32
	alpha         float32
	tex, mask     string     // texture and opacity map file names
	scale, offset [2]float32 // texture coordinate tiling and offset
}

type tdsObject struct {
	name   string
	verts  [][3]float32
	uvs    [][2]float32 // per vertex, where the object is mapped
	faces  [][3]uint16
	groups []tdsMatGroup
	smooth []uint32
	used   bool
}

type tdsNode struct {
	id, parent int
	name       string // object name, or "$$$DUMMY" for a group
	instance   string
	children   []*tdsNode
}

// chunks calls fn for each chunk in data. A chunk whose length runs past
// the end is cut short, as some exporters write bad lengths for the last
// chunk.
func chunks(data []byte, fn func(id uint16, body []byte)) {
	for len(data) >= 6 {
		id := binary.LittleEndian.Uint16(data)
		n := binary.LittleEndian.Uint32(data[2:])
		if n < 6 {
			return
		}
		if uint64(n) > uint64(len(data)) {
			n = uint32(len(data))
		}
		fn(id, data[6:n])
		data = data[n:]
	}
}

// cstring splits a NUL terminated string off the front of b.
func cstring(b []byte) (string, []byte) {
	before, after, ok := bytes.Cut(b, []byte{0})
	if !ok {
		return string(b), nil
	}
	return string(before), after
}

// loadTDS parses a 3DS file; open reads a texture named in it.
func loadTDS(data []byte, name string, open func(string) ([]byte, error)) (*step.Model, error) {
	if len(data) < 6 || binary.LittleEndian.Uint16(data) != tdsMain {
		return nil, errors.New("3ds: not a 3D Studio file")
	}
	var (
		objects []*tdsObject
		byName  = map[string]*tdsObject{}
		mats    = map[string]*tdsMat{}
		nodes   []*tdsNode
	)
	chunks(data, func(id uint16, body []byte) {
		if id != tdsMain {
			return
		}
		chunks(body, func(id uint16, body []byte) {
			switch id {
			case tdsEditor:
				chunks(body, func(id uint16, body []byte) {
					switch id {
					case tdsMaterial:
						n, m := readTDSMaterial(body)
						mats[n] = m
					case tdsNamedObj:
						if o := readTDSObject(body); o != nil {
							objects = append(objects, o)
							if _, dup := byName[o.name]; !dup {
								byName[o.name] = o
							}
						}
					}
				})
			case tdsKeyframer:
				chunks(body, func(id uint16, body []byte) {
					// Object, camera, target, light and spotlight nodes
					// share one id space; meshes can hang off any of them.
					if id >= tdsObjectNode && id <= tdsLastNode {
						nodes = append(nodes, readTDSNode(body, len(nodes)))
					}
				})
			}
		})
	})

	var warnings []string
	tx := newTextures(open, func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	})
	missing := map[string]bool{}
	meshFor := func(o *tdsObject) []*step.Mesh {
		fmat := make([]*tdsMat, len(o.faces))
		ftex := make([]*step.Texture, len(o.faces))
		for _, g := range o.groups {
			m, ok := mats[g.material]
			if !ok {
				if !missing[g.material] {
					missing[g.material] = true
					warnings = append(warnings, fmt.Sprintf("material %q not found", g.material))
				}
				continue
			}
			var tex *step.Texture
			if len(o.uvs) > 0 {
				tex = tx.load(m.tex, m.mask)
			}
			for _, f := range g.faces {
				if int(f) < len(fmat) {
					fmat[f], ftex[f] = m, tex
				}
			}
		}
		var b builder
		if len(o.smooth) == len(o.faces) {
			b.smooth = []uint32{}
		}
		bad := 0
		for i, f := range o.faces {
			if int(max(f[0], f[1], f[2])) >= len(o.verts) {
				bad++
				continue
			}
			p := [3][3]float32{o.verts[f[0]], o.verts[f[1]], o.verts[f[2]]}
			c, a, m := defaultColour, float32(1), fmat[i]
			if m != nil {
				c, a = m.colour, m.alpha
			}
			var uv *[3][2]float32
			if ftex[i] != nil && int(max(f[0], f[1], f[2])) < len(o.uvs) {
				// The texture replaces the diffuse colour. Bottom left
				// origin -> top left.
				uv = new([3][2]float32)
				for k, v := range f {
					t := o.uvs[v]
					uv[k] = [2]float32{t[0]*m.scale[0] + m.offset[0], 1 - t[1]*m.scale[1] - m.offset[1]}
				}
				c = [3]float32{1, 1, 1}
			}
			b.addTextured(p, [3][3]float32{c, c, c}, nil, ftex[i], uv, &[3]float32{a, a, a})
			if b.smooth != nil && len(b.smooth) < b.triangles() {
				b.smooth = append(b.smooth, o.smooth[i])
			}
		}
		if bad > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: %d faces with invalid vertex indices", o.name, bad))
		}
		if b.dropped > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: %d triangles with invalid coordinates", o.name, b.dropped))
		}
		return b.meshes()
	}

	// The keyframer hierarchy, where there is one.
	byID := map[int]*tdsNode{}
	for _, n := range nodes {
		byID[n.id] = n
	}
	var tops []*tdsNode
	for _, n := range nodes {
		if p := byID[n.parent]; p != nil && p != n {
			p.children = append(p.children, n)
		} else {
			tops = append(tops, n)
		}
	}
	visited := map[*tdsNode]bool{}
	var build func(n *tdsNode) *step.Node
	build = func(n *tdsNode) *step.Node {
		if visited[n] {
			return nil
		}
		visited[n] = true
		label := n.name
		if n.instance != "" {
			label = n.instance
		}
		sn := &step.Node{Name: label, Local: step.Identity()}
		// An object referenced again is an instance whose placement would
		// need the keyframer transforms; it is shown once.
		if o := byName[n.name]; o != nil && !o.used {
			o.used = true
			setMeshes(sn, meshFor(o))
		}
		for _, c := range n.children {
			if cn := build(c); cn != nil {
				sn.Children = append(sn.Children, cn)
			}
		}
		if sn.Mesh == nil && len(sn.Children) == 0 {
			return nil
		}
		return sn
	}
	var kids []*step.Node
	for _, n := range tops {
		if sn := build(n); sn != nil {
			kids = append(kids, sn)
		}
	}
	// Objects the keyframer does not mention (or no keyframer at all).
	for _, o := range objects {
		if o.used {
			continue
		}
		o.used = true
		if ms := meshFor(o); len(ms) > 0 {
			n := &step.Node{Name: o.name, Local: step.Identity()}
			setMeshes(n, ms)
			kids = append(kids, n)
		}
	}
	if len(kids) == 0 {
		return nil, errors.New("3ds: no geometry found")
	}
	root := &step.Node{Name: name, Local: step.Identity()}
	if len(kids) == 1 && len(kids[0].Children) == 0 {
		root.Mesh = kids[0].Mesh
	} else {
		root.Children = kids
	}
	return finish(name, []*step.Node{root}, warnings), nil
}

// readTDSMaterial returns a material's name, diffuse colour and textures.
func readTDSMaterial(body []byte) (string, *tdsMat) {
	var name string
	m := &tdsMat{colour: defaultColour, alpha: 1, scale: [2]float32{1, 1}}
	chunks(body, func(id uint16, body []byte) {
		switch id {
		case tdsMatName:
			name, _ = cstring(body)
		case tdsTexMap:
			chunks(body, func(id uint16, body []byte) {
				var v float32
				if len(body) >= 4 {
					v = math.Float32frombits(binary.LittleEndian.Uint32(body))
				}
				switch id {
				case tdsMapFile:
					m.tex, _ = cstring(body)
				case tdsMapUScale, tdsMapVScale:
					if v != 0 { // zero would collapse the mapping
						m.scale[(id-tdsMapUScale)/2] = v
					}
				case tdsMapUOffset, tdsMapVOffset:
					m.offset[(id-tdsMapUOffset)/2] = v
				}
			})
		case tdsOpacMap:
			chunks(body, func(id uint16, body []byte) {
				if id == tdsMapFile {
					m.mask, _ = cstring(body)
				}
			})
		case tdsTransp:
			chunks(body, func(id uint16, body []byte) {
				switch {
				case id == tdsPercentI && len(body) >= 2:
					m.alpha = 1 - float32(int16(binary.LittleEndian.Uint16(body)))/100
				case id == tdsPercentF && len(body) >= 4:
					m.alpha = 1 - math.Float32frombits(binary.LittleEndian.Uint32(body))/100
				}
			})
		case tdsDiffuse:
			// Prefer the plain colours over the gamma corrected ones.
			best := 0
			chunks(body, func(id uint16, body []byte) {
				rank := map[uint16]int{tdsColor24: 4, tdsColorF: 3, tdsLinColor24: 2, tdsLinColorF: 1}[id]
				if rank <= best {
					return
				}
				switch {
				case (id == tdsColor24 || id == tdsLinColor24) && len(body) >= 3:
					m.colour = [3]float32{float32(body[0]) / 255, float32(body[1]) / 255, float32(body[2]) / 255}
				case (id == tdsColorF || id == tdsLinColorF) && len(body) >= 12:
					for k := range 3 {
						v := math.Float32frombits(binary.LittleEndian.Uint32(body[k*4:]))
						if math.IsNaN(float64(v)) {
							v = 0
						}
						m.colour[k] = min(max(v, 0), 1)
					}
				default:
					return
				}
				best = rank
			})
		}
	})
	return name, m
}

// readTDSObject reads a named object, or returns nil if it is not a
// triangle mesh (a camera or light, say).
func readTDSObject(body []byte) *tdsObject {
	name, rest := cstring(body)
	var o *tdsObject
	chunks(rest, func(id uint16, body []byte) {
		if id != tdsTriMesh || o != nil {
			return
		}
		o = &tdsObject{name: name}
		chunks(body, func(id uint16, body []byte) {
			switch id {
			case tdsVertices:
				if len(body) < 2 {
					return
				}
				n := min(int(binary.LittleEndian.Uint16(body)), (len(body)-2)/12)
				o.verts = make([][3]float32, n)
				for i := range n {
					for k := range 3 {
						o.verts[i][k] = math.Float32frombits(binary.LittleEndian.Uint32(body[2+i*12+k*4:]))
					}
				}
			case tdsUV:
				if len(body) < 2 {
					return
				}
				n := min(int(binary.LittleEndian.Uint16(body)), (len(body)-2)/8)
				o.uvs = make([][2]float32, n)
				for i := range n {
					for k := range 2 {
						o.uvs[i][k] = math.Float32frombits(binary.LittleEndian.Uint32(body[2+i*8+k*4:]))
					}
				}
			case tdsFaces:
				if len(body) < 2 {
					return
				}
				n := min(int(binary.LittleEndian.Uint16(body)), (len(body)-2)/8)
				o.faces = make([][3]uint16, n)
				for i := range n {
					f := body[2+i*8:]
					o.faces[i] = [3]uint16{binary.LittleEndian.Uint16(f), binary.LittleEndian.Uint16(f[2:]), binary.LittleEndian.Uint16(f[4:])}
				}
				chunks(body[2+n*8:], func(id uint16, body []byte) {
					switch id {
					case tdsFaceMat:
						mat, rest := cstring(body)
						if len(rest) < 2 {
							return
						}
						k := min(int(binary.LittleEndian.Uint16(rest)), (len(rest)-2)/2)
						g := tdsMatGroup{material: mat, faces: make([]uint16, k)}
						for i := range k {
							g.faces[i] = binary.LittleEndian.Uint16(rest[2+i*2:])
						}
						o.groups = append(o.groups, g)
					case tdsSmooth:
						k := len(body) / 4
						o.smooth = make([]uint32, k)
						for i := range k {
							o.smooth[i] = binary.LittleEndian.Uint32(body[i*4:])
						}
					}
				})
			}
		})
	})
	return o
}

// readTDSNode reads a keyframer object node; index is its default id.
func readTDSNode(body []byte, index int) *tdsNode {
	n := &tdsNode{id: index, parent: -1}
	chunks(body, func(id uint16, body []byte) {
		switch id {
		case tdsNodeID:
			if len(body) >= 2 {
				n.id = int(binary.LittleEndian.Uint16(body))
			}
		case tdsNodeHeader:
			name, rest := cstring(body)
			n.name = name
			if len(rest) >= 6 {
				n.parent = int(int16(binary.LittleEndian.Uint16(rest[4:])))
			}
		case tdsInstance:
			n.instance, _ = cstring(body)
		}
	})
	return n
}
