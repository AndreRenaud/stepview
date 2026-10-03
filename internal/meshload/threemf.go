package meshload

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"

	"github.com/AndreRenaud/stepview/internal/step"
)

// Load3MF reads a 3MF file: the build items of its root model, with their
// components (including production extension references into other model
// parts), colours from base materials and colour groups, and textures.
func Load3MF(path string) (*step.Model, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return load3MF(&zr.Reader, baseName(path))
}

// Components can reuse an object many times over, so a small file can
// expand into a huge tree; these bound it.
const (
	maxNodes     = 1 << 20
	maxTriangles = 50_000_000 // counting every instance
)

var units = map[string]float64{
	"micron":     0.001,
	"millimeter": 1,
	"centimeter": 10,
	"inch":       25.4,
	"foot":       304.8,
	"meter":      1000,
}

type tmfTri struct {
	v   [3]int
	pid int    // -1 when unset
	p   [3]int // -1 when unset
}

type tmfComp struct {
	object int
	path   string // model part, or "" for the same one
	xf     step.Affine
}

type tmfObject struct {
	id, pid, pindex int
	name, typ       string
	verts           [][3]float32
	tris            []tmfTri
	comps           []tmfComp
}

type tmfItem struct {
	object int
	path   string
	xf     step.Affine
}

// tmfTexGroup is a texture coordinate group (texture2dgroup).
type tmfTexGroup struct {
	texture int // texture2d id
	uv      [][2]float32
}

type tmfModel struct {
	unit      float64
	objects   map[int]*tmfObject
	order     []*tmfObject
	props     map[int][][3]float32 // property group -> colours
	textures  map[int]string       // texture2d id -> image part
	texGroups map[int]*tmfTexGroup
	build     []tmfItem
}

type tmfLoader struct {
	files    map[string]*zip.File // lower-case part name without leading slash
	models   map[string]*tmfModel
	meshes   map[*tmfObject][]*step.Mesh
	textures *textures
	nodes    int
	tris     int
	warnings []string
}

func load3MF(zr *zip.Reader, name string) (*step.Model, error) {
	l := &tmfLoader{
		files:  map[string]*zip.File{},
		models: map[string]*tmfModel{},
		meshes: map[*tmfObject][]*step.Mesh{},
	}
	for _, f := range zr.File {
		l.files[partKey(f.Name)] = f
	}
	l.textures = newTextures(l.readPart, l.warn)
	rootPath := l.rootPart()
	root, err := l.model(rootPath)
	if err != nil {
		return nil, err
	}
	items := root.build
	if len(items) == 0 {
		// No build section: show every object that is not a component.
		used := map[int]bool{}
		for _, o := range root.order {
			for _, c := range o.comps {
				if c.path == "" {
					used[c.object] = true
				}
			}
		}
		for _, o := range root.order {
			if !used[o.id] {
				items = append(items, tmfItem{object: o.id, xf: step.Identity()})
			}
		}
	}
	s := root.unit
	top := &step.Node{Name: name, Local: step.Affine{R: [3][3]float64{{s, 0, 0}, {0, s, 0}, {0, 0, s}}}}
	for _, it := range items {
		p := rootPath
		if it.path != "" {
			p = it.path
		}
		if n := l.node(p, it.object, it.xf, 0); n != nil {
			top.Children = append(top.Children, n)
		}
	}
	if len(top.Children) == 0 {
		return nil, errors.New("3mf: no geometry found")
	}
	return finish(name, []*step.Node{top}, l.warnings), nil
}

func partKey(name string) string {
	return strings.ToLower(strings.TrimPrefix(name, "/"))
}

// rootPart finds the root model part from the package relationships.
func (l *tmfLoader) rootPart() string {
	if f := l.files["_rels/.rels"]; f != nil {
		if rc, err := f.Open(); err == nil {
			defer rc.Close()
			var rels struct {
				Rel []struct {
					Target string `xml:"Target,attr"`
					Type   string `xml:"Type,attr"`
				} `xml:"Relationship"`
			}
			if xml.NewDecoder(rc).Decode(&rels) == nil {
				for _, r := range rels.Rel {
					if strings.HasSuffix(r.Type, "/3dmodel") {
						return r.Target
					}
				}
			}
		}
	}
	return "3D/3dmodel.model"
}

func (l *tmfLoader) model(part string) (*tmfModel, error) {
	key := partKey(part)
	if m, ok := l.models[key]; ok {
		if m == nil {
			return nil, fmt.Errorf("3mf: model part %s is unreadable", part)
		}
		return m, nil
	}
	l.models[key] = nil
	f := l.files[key]
	if f == nil {
		return nil, fmt.Errorf("3mf: missing model part %s", part)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("3mf: %s: %v", part, err)
	}
	defer rc.Close()
	m, err := parseModel(rc)
	if err != nil {
		return nil, fmt.Errorf("3mf: %s: %v", part, err)
	}
	l.models[key] = m
	return m, nil
}

// maxPartSize bounds the parts that are read whole, such as textures.
const maxPartSize = 1 << 28

// readPart reads a part of the package, such as a texture.
func (l *tmfLoader) readPart(name string) ([]byte, error) {
	f := l.files[partKey(name)]
	if f == nil {
		return nil, fs.ErrNotExist
	}
	if f.UncompressedSize64 > maxPartSize {
		return nil, errors.New("part is too large")
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, maxPartSize))
}

func (l *tmfLoader) warn(format string, args ...any) {
	if len(l.warnings) < 1000 {
		l.warnings = append(l.warnings, fmt.Sprintf(format, args...))
	}
}

// node builds the tree for object id in the given model part.
func (l *tmfLoader) node(part string, id int, xf step.Affine, depth int) *step.Node {
	if depth > 32 || l.nodes >= maxNodes || l.tris >= maxTriangles {
		l.warn("%s: object %d: components nested too deeply or too many", part, id)
		return nil
	}
	m, err := l.model(part)
	if err != nil {
		l.warn("%v", err)
		return nil
	}
	o := m.objects[id]
	if o == nil {
		l.warn("%s: object %d not found", part, id)
		return nil
	}
	if o.typ == "support" || o.typ == "other" {
		return nil
	}
	l.nodes++
	name := o.name
	if name == "" {
		name = fmt.Sprintf("Object %d", id)
	}
	n := &step.Node{Name: name, Local: xf}
	if len(o.tris) > 0 {
		ms := l.mesh(part, m, o)
		for _, me := range ms {
			l.tris += me.TriangleCount()
		}
		setMeshes(n, ms)
	}
	for _, c := range o.comps {
		p := part
		if c.path != "" {
			p = c.path
		}
		if cn := l.node(p, c.object, c.xf, depth+1); cn != nil {
			n.Children = append(n.Children, cn)
		}
	}
	if n.Mesh == nil && len(n.Children) == 0 {
		return nil
	}
	return n
}

// mesh builds an object's meshes, one per texture.
func (l *tmfLoader) mesh(part string, m *tmfModel, o *tmfObject) []*step.Mesh {
	if me, ok := l.meshes[o]; ok {
		return me
	}
	colour := func(pid, idx int) [3]float32 {
		if g := m.props[pid]; idx >= 0 && idx < len(g) {
			return g[idx]
		}
		return defaultColour
	}
	var b builder
	bad := 0
	for _, t := range o.tris {
		var p, c [3][3]float32
		ok := true
		for k, v := range t.v {
			if v < 0 || v >= len(o.verts) {
				ok = false
				break
			}
			p[k] = o.verts[v]
		}
		if !ok {
			bad++
			continue
		}
		pid, idx := t.pid, t.p
		if pid < 0 {
			pid = o.pid
		}
		if idx[0] < 0 {
			idx[0] = o.pindex
		}
		for k := range 3 {
			if idx[k] < 0 {
				idx[k] = idx[0]
			}
			c[k] = colour(pid, idx[k])
		}
		if g := m.texGroups[pid]; g != nil {
			if tex, uv, ok := l.texCoords(m, g, idx); ok {
				w := [3]float32{1, 1, 1}
				b.addTextured(p, [3][3]float32{w, w, w}, nil, tex, &uv)
				continue
			}
		}
		b.add(p, c, nil)
	}
	if bad > 0 {
		l.warn("%s: object %d: %d triangles with invalid vertex indices", part, o.id, bad)
	}
	if b.dropped > 0 {
		l.warn("%s: object %d: %d triangles with invalid coordinates", part, o.id, b.dropped)
	}
	ms := b.meshes()
	l.meshes[o] = ms
	return ms
}

// texCoords returns the texture and corner coordinates for a triangle
// whose properties are in texture coordinate group g.
func (l *tmfLoader) texCoords(m *tmfModel, g *tmfTexGroup, idx [3]int) (*step.Texture, [3][2]float32, bool) {
	var uv [3][2]float32
	for k, i := range idx {
		if i < 0 || i >= len(g.uv) {
			return nil, uv, false
		}
		// Bottom left origin -> top left.
		uv[k] = [2]float32{g.uv[i][0], 1 - g.uv[i][1]}
	}
	path, ok := m.textures[g.texture]
	if !ok {
		return nil, uv, false
	}
	tex := l.textures.load(path, "")
	return tex, uv, tex != nil
}

// parseModel reads a 3MF model part. Namespace prefixes are ignored: the
// element and attribute names used here are unique across the core,
// material and production specifications.
func parseModel(r io.Reader) (*tmfModel, error) {
	m := &tmfModel{
		unit:      1,
		objects:   map[int]*tmfObject{},
		props:     map[int][][3]float32{},
		textures:  map[int]string{},
		texGroups: map[int]*tmfTexGroup{},
	}
	d := xml.NewDecoder(r)
	var obj *tmfObject
	var texGroup *tmfTexGroup
	group := -1
	inBuild := false
	for {
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			a := attrs(t.Attr)
			switch t.Name.Local {
			case "model":
				if u, ok := units[a.str("unit")]; ok {
					m.unit = u
				}
			case "basematerials", "colorgroup", "compositematerials", "multiproperties":
				group = a.int("id", -1)
				if _, ok := m.props[group]; !ok {
					m.props[group] = nil
				}
			case "texture2d":
				m.textures[a.int("id", -1)] = a.str("path")
			case "texture2dgroup":
				texGroup = &tmfTexGroup{texture: a.int("texid", -1)}
				if id := a.int("id", -1); m.texGroups[id] == nil {
					m.texGroups[id] = texGroup
				}
			case "tex2coord":
				if texGroup != nil {
					texGroup.uv = append(texGroup.uv, [2]float32{a.f32("u"), a.f32("v")})
				}
			case "base":
				if group >= 0 {
					m.props[group] = append(m.props[group], parseColour(a.str("displaycolor")))
				}
			case "color":
				if group >= 0 {
					m.props[group] = append(m.props[group], parseColour(a.str("color")))
				}
			case "object":
				obj = &tmfObject{
					id:     a.int("id", -1),
					pid:    a.int("pid", -1),
					pindex: a.int("pindex", 0),
					name:   a.str("name"),
					typ:    a.str("type"),
				}
				if _, dup := m.objects[obj.id]; !dup {
					m.objects[obj.id] = obj
					m.order = append(m.order, obj)
				}
			case "vertex":
				if obj != nil {
					obj.verts = append(obj.verts, [3]float32{a.f32("x"), a.f32("y"), a.f32("z")})
				}
			case "triangle":
				if obj != nil {
					obj.tris = append(obj.tris, tmfTri{
						v:   [3]int{a.int("v1", -1), a.int("v2", -1), a.int("v3", -1)},
						pid: a.int("pid", -1),
						p:   [3]int{a.int("p1", -1), a.int("p2", -1), a.int("p3", -1)},
					})
				}
			case "component":
				if obj != nil {
					obj.comps = append(obj.comps, tmfComp{object: a.int("objectid", -1), path: a.str("path"), xf: parseTransform(a.str("transform"))})
				}
			case "build":
				inBuild = true
			case "item":
				if inBuild {
					m.build = append(m.build, tmfItem{object: a.int("objectid", -1), path: a.str("path"), xf: parseTransform(a.str("transform"))})
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "object":
				obj = nil
			case "basematerials", "colorgroup", "compositematerials", "multiproperties":
				group = -1
			case "texture2dgroup":
				texGroup = nil
			case "build":
				inBuild = false
			}
		}
	}
	return m, nil
}

type attrs []xml.Attr

func (a attrs) str(name string) string {
	for _, x := range a {
		if x.Name.Local == name {
			return x.Value
		}
	}
	return ""
}

func (a attrs) int(name string, def int) int {
	if v, err := strconv.Atoi(a.str(name)); err == nil {
		return v
	}
	return def
}

func (a attrs) f32(name string) float32 {
	v, _ := strconv.ParseFloat(a.str(name), 32)
	return float32(v)
}

// parseTransform reads a 3MF matrix: twelve numbers, a 4x3 matrix applied
// to row vectors, so its transpose is the rotation of p' = R*p + T.
func parseTransform(s string) step.Affine {
	f := strings.Fields(s)
	if len(f) != 12 {
		return step.Identity()
	}
	var m [12]float64
	for i, v := range f {
		x, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return step.Identity()
		}
		m[i] = x
	}
	return step.Affine{
		R: [3][3]float64{
			{m[0], m[3], m[6]},
			{m[1], m[4], m[7]},
			{m[2], m[5], m[8]},
		},
		T: step.Vec3{X: m[9], Y: m[10], Z: m[11]},
	}
}

// parseColour reads an sRGB colour written as #RRGGBB or #RRGGBBAA.
func parseColour(s string) [3]float32 {
	if len(s) != 7 && len(s) != 9 || s[0] != '#' {
		return defaultColour
	}
	v, err := strconv.ParseUint(s[1:7], 16, 32)
	if err != nil {
		return defaultColour
	}
	return [3]float32{float32(v>>16&255) / 255, float32(v>>8&255) / 255, float32(v&255) / 255}
}
