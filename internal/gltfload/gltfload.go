// Package gltfload converts glTF 2.0 files into the viewer's model
// representation (the same one produced by the step package).
package gltfload

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/modeler"

	"github.com/AndreRenaud/stepview/internal/meshload"
	"github.com/AndreRenaud/stepview/internal/step"
)

// LoadFile reads a .gltf or .glb file. glTF uses metres with Y up; the
// result is converted to millimetres with Z up to match STEP data.
func LoadFile(path string) (*step.Model, error) {
	doc, err := gltf.Open(path)
	if err != nil {
		return nil, err
	}
	return load(doc, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), os.DirFS(filepath.Dir(path)))
}

// load converts a decoded document; name labels the root node and fsys
// holds the external images it refers to.
func load(doc *gltf.Document, name string, fsys fs.FS) (*step.Model, error) {
	l := &loader{
		doc:      doc,
		fsys:     fsys,
		meshes:   map[int][]*step.Mesh{},
		images:   map[int]*step.Texture{},
		variants: map[variant]*step.Texture{},
	}
	// Y-up metres -> Z-up millimetres.
	conv := step.Affine{R: [3][3]float64{{1000, 0, 0}, {0, 0, -1000}, {0, 1000, 0}}}
	root := &step.Node{Name: name, Local: conv}
	sceneIdx := 0
	if doc.Scene != nil {
		sceneIdx = *doc.Scene
	}
	var nodes []int
	if sceneIdx >= 0 && sceneIdx < len(doc.Scenes) {
		nodes = doc.Scenes[sceneIdx].Nodes
	} else {
		// No scenes: treat every node that is not a child as a root.
		child := map[int]bool{}
		for _, n := range doc.Nodes {
			for _, c := range n.Children {
				child[c] = true
			}
		}
		for i := range doc.Nodes {
			if !child[i] {
				nodes = append(nodes, i)
			}
		}
	}
	for _, ni := range nodes {
		if n := l.node(ni, 0); n != nil {
			root.Children = append(root.Children, n)
		}
	}
	if len(root.Children) == 0 {
		return nil, errors.New("gltf: no geometry found")
	}
	m := &step.Model{Name: name, Roots: []*step.Node{root}}
	var count func(n *step.Node)
	count = func(n *step.Node) {
		if n.Mesh != nil {
			m.Stats.Triangles += n.Mesh.TriangleCount()
			m.Stats.Solids++
		}
		for _, c := range n.Children {
			count(c)
		}
	}
	count(root)
	m.Warnings = l.warnings
	return m, nil
}

type loader struct {
	doc      *gltf.Document
	fsys     fs.FS
	meshes   map[int][]*step.Mesh
	images   map[int]*step.Texture // decoded images, nil where unusable
	variants map[variant]*step.Texture
	warnings []string
}

// variant is an image with its Cutout and Blend flags set by a material's
// alpha mode rather than by the image itself.
type variant struct {
	image int
	mode  gltf.AlphaMode
}

func nodeTransform(n *gltf.Node) step.Affine {
	m := n.MatrixOrDefault()
	if m != gltf.DefaultMatrix {
		// Column-major 4x4.
		return step.Affine{
			R: [3][3]float64{
				{m[0], m[4], m[8]},
				{m[1], m[5], m[9]},
				{m[2], m[6], m[10]},
			},
			T: step.Vec3{X: m[12], Y: m[13], Z: m[14]},
		}
	}
	q := n.RotationOrDefault()
	s := n.ScaleOrDefault()
	t := n.TranslationOrDefault()
	x, y, z, w := q[0], q[1], q[2], q[3]
	r := [3][3]float64{
		{1 - 2*(y*y+z*z), 2 * (x*y - z*w), 2 * (x*z + y*w)},
		{2 * (x*y + z*w), 1 - 2*(x*x+z*z), 2 * (y*z - x*w)},
		{2 * (x*z - y*w), 2 * (y*z + x*w), 1 - 2*(x*x+y*y)},
	}
	for i := range 3 {
		for j := range 3 {
			r[i][j] *= s[j]
		}
	}
	return step.Affine{R: r, T: step.Vec3{X: t[0], Y: t[1], Z: t[2]}}
}

func (l *loader) node(i, depth int) *step.Node {
	if i < 0 || i >= len(l.doc.Nodes) || depth > 64 {
		return nil
	}
	gn := l.doc.Nodes[i]
	name := gn.Name
	if name == "" {
		name = fmt.Sprintf("Node %d", i)
	}
	n := &step.Node{Name: name, Local: nodeTransform(gn)}
	if gn.Mesh != nil {
		meshes := l.mesh(*gn.Mesh)
		if len(meshes) == 1 && len(gn.Children) == 0 {
			n.Mesh = meshes[0]
		} else {
			for k, m := range meshes {
				n.Children = append(n.Children, &step.Node{Name: fmt.Sprintf("%s.%d", name, k), Local: step.Identity(), Mesh: m})
			}
		}
	}
	for _, c := range gn.Children {
		if cn := l.node(c, depth+1); cn != nil {
			n.Children = append(n.Children, cn)
		}
	}
	if n.Mesh == nil && len(n.Children) == 0 {
		return nil
	}
	return n
}

func (l *loader) mesh(i int) []*step.Mesh {
	if m, ok := l.meshes[i]; ok {
		return m
	}
	var out []*step.Mesh
	if i >= 0 && i < len(l.doc.Meshes) {
		for _, p := range l.doc.Meshes[i].Primitives {
			m, err := l.primitive(p)
			if err != nil {
				l.warnings = append(l.warnings, fmt.Sprintf("mesh %d: %v", i, err))
				continue
			}
			out = append(out, m)
		}
	}
	l.meshes[i] = out
	return out
}

func (l *loader) primitive(p *gltf.Primitive) (*step.Mesh, error) {
	if p.Mode != gltf.PrimitiveTriangles {
		return nil, fmt.Errorf("unsupported primitive mode %d", p.Mode)
	}
	pi, ok := p.Attributes[gltf.POSITION]
	if !ok {
		return nil, errors.New("no positions")
	}
	pa, err := l.accessor(pi)
	if err != nil {
		return nil, err
	}
	pos, err := modeler.ReadPosition(l.doc, pa, nil)
	if err != nil {
		return nil, err
	}
	var idx []uint32
	if p.Indices != nil {
		ia, err := l.accessor(*p.Indices)
		if err != nil {
			return nil, err
		}
		idx, err = modeler.ReadIndices(l.doc, ia, nil)
		if err != nil {
			return nil, err
		}
	} else {
		idx = make([]uint32, len(pos))
		for k := range idx {
			idx[k] = uint32(k)
		}
	}
	var nrm [][3]float32
	if i, ok := p.Attributes[gltf.NORMAL]; ok {
		if a, err := l.accessor(i); err == nil {
			nrm, _ = modeler.ReadNormal(l.doc, a, nil)
		}
	}
	base := [4]float64{0.8, 0.8, 0.8, 1}
	var pbr *gltf.PBRMetallicRoughness
	var tex *step.Texture
	var uvs [][2]float32
	blend := false
	if p.Material != nil && *p.Material >= 0 && *p.Material < len(l.doc.Materials) && l.doc.Materials[*p.Material] != nil {
		mat := l.doc.Materials[*p.Material]
		pbr = mat.PBRMetallicRoughness
		blend = mat.AlphaMode == gltf.AlphaBlend
		if pbr != nil && pbr.BaseColorFactor != nil {
			base = *pbr.BaseColorFactor
		}
		var set int
		if tex, set = l.texture(mat); tex != nil {
			if a, err := l.accessor(attribute(p, fmt.Sprintf("TEXCOORD_%d", set))); err == nil {
				uvs, _ = modeler.ReadTextureCoord(l.doc, a, nil)
			}
			if len(uvs) != len(pos) {
				l.warnings = append(l.warnings, fmt.Sprintf("texture %s: no texture coordinates", tex.Name))
				tex = nil
			}
		}
	}
	var cols [][4]uint8
	if i, ok := p.Attributes[gltf.COLOR_0]; ok {
		if a, err := l.accessor(i); err == nil {
			cols, _ = modeler.ReadColor(l.doc, a, nil)
		}
	}
	m := &step.Mesh{Bounds: step.EmptyBox()}
	if pbr != nil {
		m.Metallic = float32(pbr.MetallicFactorOrDefault())
		m.Roughness = float32(pbr.RoughnessFactorOrDefault())
		m.HasPBR = true
	}
	m.Positions = make([]float32, 0, len(pos)*3)
	for _, v := range pos {
		m.Positions = append(m.Positions, v[0], v[1], v[2])
		m.Bounds.Extend(step.Vec3{X: float64(v[0]), Y: float64(v[1]), Z: float64(v[2])})
	}
	if len(nrm) != len(pos) {
		nrm = computeNormals(pos, idx)
	}
	m.Normals = make([]float32, 0, len(pos)*3)
	for _, v := range nrm {
		m.Normals = append(m.Normals, v[0], v[1], v[2])
	}
	m.Colors = make([]float32, 0, len(pos)*3)
	for k := range pos {
		c := [3]float32{float32(base[0]), float32(base[1]), float32(base[2])}
		if k < len(cols) {
			for j := range 3 {
				c[j] *= float32(cols[k][j]) / 255
			}
		}
		// glTF base colours are linear; convert to sRGB-ish for display.
		for j := range 3 {
			c[j] = float32(math.Pow(float64(c[j]), 1/2.2))
		}
		m.Colors = append(m.Colors, c[0], c[1], c[2])
	}
	if blend {
		// Opacity is the base colour factor's alpha times the vertex
		// colour's.
		m.Alpha = make([]float32, len(pos))
		for k := range pos {
			a := base[3]
			if k < len(cols) {
				a *= float64(cols[k][3]) / 255
			}
			m.Alpha[k] = 1
			if a < 1 {
				m.Alpha[k] = float32(max(a, 0))
			}
		}
		if !slices.ContainsFunc(m.Alpha, func(a float32) bool { return a < 1 }) {
			m.Alpha = nil
		}
	}
	if tex != nil {
		m.Texture = tex
		m.UVs = make([]float32, 0, len(uvs)*2)
		for _, uv := range uvs {
			for _, v := range uv {
				if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
					v = 0
				}
				m.UVs = append(m.UVs, v)
			}
		}
	}
	for k := 0; k+2 < len(idx); k += 3 {
		if int(idx[k]) >= len(pos) || int(idx[k+1]) >= len(pos) || int(idx[k+2]) >= len(pos) {
			continue
		}
		m.Indices = append(m.Indices, idx[k], idx[k+1], idx[k+2])
	}
	m.FaceStarts = []uint32{0}
	return m, nil
}

// attribute returns the accessor index of a primitive attribute, or -1.
func attribute(p *gltf.Primitive, name string) int {
	if i, ok := p.Attributes[name]; ok {
		return i
	}
	return -1
}

// texture returns a material's base colour texture and the index of the
// texture coordinate set it uses, or nil. Its alpha follows the material's
// alpha mode: masks cut holes (Cutout), blending makes it opacity (Blend),
// and opaque materials ignore it.
func (l *loader) texture(mat *gltf.Material) (*step.Texture, int) {
	pbr := mat.PBRMetallicRoughness
	if pbr == nil || pbr.BaseColorTexture == nil {
		return nil, 0
	}
	ti := pbr.BaseColorTexture.Index
	if ti < 0 || ti >= len(l.doc.Textures) || l.doc.Textures[ti] == nil || l.doc.Textures[ti].Source == nil {
		return nil, 0
	}
	src := *l.doc.Textures[ti].Source
	tex := l.image(src)
	if tex == nil {
		return nil, 0
	}
	cutout, blend := mat.AlphaMode == gltf.AlphaMask, mat.AlphaMode == gltf.AlphaBlend
	if tex.Cutout != cutout || tex.Blend != blend {
		v := variant{src, mat.AlphaMode}
		if l.variants[v] == nil {
			c := *tex
			c.Cutout, c.Blend = cutout, blend
			l.variants[v] = &c
		}
		tex = l.variants[v]
	}
	return tex, pbr.BaseColorTexture.TexCoord
}

// image decodes the i'th image, or returns nil (with a warning) if it is
// unusable.
func (l *loader) image(i int) *step.Texture {
	if tex, ok := l.images[i]; ok {
		return tex
	}
	l.images[i] = nil
	if i < 0 || i >= len(l.doc.Images) || l.doc.Images[i] == nil {
		l.warnings = append(l.warnings, fmt.Sprintf("image %d not found", i))
		return nil
	}
	im := l.doc.Images[i]
	name := im.Name
	if name == "" && !strings.HasPrefix(im.URI, "data:") {
		name = im.URI
	}
	if name == "" {
		name = fmt.Sprintf("image %d", i)
	}
	data, err := l.imageData(im)
	if err != nil {
		l.warnings = append(l.warnings, fmt.Sprintf("texture %s: %v", name, err))
		return nil
	}
	tex, err := meshload.DecodeTexture(data, name)
	if err != nil {
		l.warnings = append(l.warnings, err.Error())
		return nil
	}
	l.images[i] = tex
	return tex
}

// imageData returns an image's encoded bytes, from a buffer view, a data
// URI or an external file.
func (l *loader) imageData(im *gltf.Image) ([]byte, error) {
	switch {
	case im.BufferView != nil:
		i := *im.BufferView
		if i < 0 || i >= len(l.doc.BufferViews) || l.doc.BufferViews[i] == nil {
			return nil, fmt.Errorf("buffer view %d out of range", i)
		}
		bv := l.doc.BufferViews[i]
		if bv.Buffer < 0 || bv.Buffer >= len(l.doc.Buffers) || l.doc.Buffers[bv.Buffer] == nil {
			return nil, fmt.Errorf("buffer %d out of range", bv.Buffer)
		}
		data := l.doc.Buffers[bv.Buffer].Data
		if bv.ByteOffset < 0 || bv.ByteLength < 0 || bv.ByteOffset > len(data) || bv.ByteLength > len(data)-bv.ByteOffset {
			return nil, errors.New("buffer view out of range")
		}
		return data[bv.ByteOffset : bv.ByteOffset+bv.ByteLength], nil
	case strings.HasPrefix(im.URI, "data:"):
		_, enc, ok := strings.Cut(im.URI, ";base64,")
		if !ok {
			return nil, errors.New("unsupported data URI")
		}
		return base64.StdEncoding.DecodeString(enc)
	case l.fsys == nil:
		return nil, errors.New("external images are not supported here")
	}
	name, err := url.PathUnescape(im.URI)
	if err != nil {
		name = im.URI
	}
	return fs.ReadFile(l.fsys, name)
}

// accessor returns the i'th accessor, or an error if there is none.
func (l *loader) accessor(i int) (*gltf.Accessor, error) {
	if i < 0 || i >= len(l.doc.Accessors) || l.doc.Accessors[i] == nil {
		return nil, fmt.Errorf("accessor %d out of range", i)
	}
	return l.doc.Accessors[i], nil
}

func computeNormals(pos [][3]float32, idx []uint32) [][3]float32 {
	acc := make([]step.Vec3, len(pos))
	v := func(i uint32) step.Vec3 {
		return step.Vec3{X: float64(pos[i][0]), Y: float64(pos[i][1]), Z: float64(pos[i][2])}
	}
	for k := 0; k+2 < len(idx); k += 3 {
		a, b, c := idx[k], idx[k+1], idx[k+2]
		if int(max(a, b, c)) >= len(pos) {
			continue
		}
		n := v(b).Sub(v(a)).Cross(v(c).Sub(v(a)))
		acc[a] = acc[a].Add(n)
		acc[b] = acc[b].Add(n)
		acc[c] = acc[c].Add(n)
	}
	out := make([][3]float32, len(pos))
	for i, n := range acc {
		n = n.Norm()
		out[i] = [3]float32{float32(n.X), float32(n.Y), float32(n.Z)}
	}
	return out
}
