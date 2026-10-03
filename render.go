package main

import (
	"image"
	"image/draw"
	"math"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/AndreRenaud/stepview/internal/step"
)

// The renderer draws all visible triangles with a depth test, although
// Ebitengine has no depth buffer. Each vertex carries its inverse depth,
// which (unlike depth itself) interpolates linearly across a triangle on
// screen. The nearest surface's 24-bit inverse depth is found one byte at a
// time, in three passes that draw every triangle into an 8-bit image with
// a "max" blend: the first pass keeps the highest top byte, the second the
// highest middle byte among fragments matching that top byte, and the third
// likewise for the low byte, storing all three bytes so that the third
// image holds the whole depth. Further passes draw the fragments whose
// depth equals the stored one: in normal mode, a single pass of lit
// colour; in high quality mode, the surface colour and material and then
// the normal, for the screen-space passes in hq.go. Every pass sees
// identical vertices, so a fragment's interpolated depth is identical in
// each of them.
//
// Projection runs on the CPU, spread over all cores.

const geometryShaderSource = `//kage:unit pixels

package main

// Stage is the pass: 0-2 find the depth, 3 draws the visible surfaces.
var Stage int

// Output is what stage 3 writes: 0 the lit colour, 1 the colour and
// material code, 2 the encoded view-space normal.
var Output int

// Textured is 1 when the triangles are textured by image 3, with srcPos
// holding the texture coordinates divided by depth. Cutout is 1 when the
// texture's transparent parts are holes, discarded in every pass; each
// pass must then decide alike, which holds as the depth images are
// unmanaged (their origin is zero, so srcPos is the same in every pass).
// DepthA and DepthB decode the inverse depth: 1/z = custom.x*DepthA +
// DepthB.
var Textured int
var Cutout int
var DepthA float
var DepthB float

// Right, Up and Back are the camera's axes in model coordinates.
var Right vec3
var Up vec3
var Back vec3

func Fragment(dstPos vec4, srcPos vec2, color vec4, custom vec4) vec4 {
	tex := vec4(1)
	if Textured == 1 {
		// Texture coordinates divided by depth interpolate linearly on
		// screen, like the inverse depth itself.
		tex = sampleTexture((srcPos - imageSrc0Origin()) / (custom.x*DepthA + DepthB))
		if Cutout == 1 && tex.a < 0.5 {
			discard()
		}
		if tex.a > 0 {
			tex.rgb /= tex.a
		}
	}
	color.rgb *= tex.rgb

	q := floor(clamp(custom.x, 0, 1) * 16777215)
	hi := floor(q / 65536)
	q -= hi * 65536
	mid := floor(q / 256)
	lo := q - mid*256

	if Stage == 0 {
		return vec4(hi / 255)
	}
	p := dstPos.xy - imageDstOrigin()
	if Stage == 1 {
		if abs(imageSrc0UnsafeAt(p+imageSrc0Origin()).r*255-hi) > 0.5 {
			return vec4(0)
		}
		return vec4(mid / 255)
	}
	if Stage == 2 {
		if abs(imageSrc0UnsafeAt(p+imageSrc0Origin()).r*255-hi) > 0.5 ||
			abs(imageSrc1UnsafeAt(p+imageSrc1Origin()).r*255-mid) > 0.5 {
			return vec4(0)
		}
		return vec4(lo, hi, mid, 255) / 255
	}
	d := imageSrc0UnsafeAt(p+imageSrc0Origin()) * 255
	if abs(d.r-lo) > 0.5 || abs(d.g-hi) > 0.5 || abs(d.b-mid) > 0.5 {
		discard()
	}
	n := normalize(custom.yzw)
	if Output == 1 {
		return color
	}
	if Output == 2 {
		return encodeNormal(vec3(dot(n, Right), dot(n, Up), dot(n, Back)))
	}
	// A fixed lighting rig in model coordinates (Z up): a key light from
	// the upper front right, a fill from the left rear and a weak light
	// from below.
	l := 0.3 +
		max(dot(n, normalize(vec3(0.5, -0.6, 0.75))), 0)*0.65 +
		max(dot(n, normalize(vec3(-0.7, 0.5, 0.3))), 0)*0.35 +
		max(dot(n, normalize(vec3(0.1, 0.3, -1))), 0)*0.15
	return vec4(min(color.rgb*l, 1), 1)
}

// sampleTexture filters image 3 bilinearly at uv, in units of the image
// size, repeating it outside [0, 1].
func sampleTexture(uv vec2) vec4 {
	size := imageSrc3Size()
	p := uv*size - 0.5
	i := floor(p)
	f := p - i
	return mix(
		mix(texel(i, size), texel(i+vec2(1, 0), size), f.x),
		mix(texel(i+vec2(0, 1), size), texel(i+vec2(1, 1), size), f.x),
		f.y)
}

// texel returns pixel i of image 3, wrapping around its edges.
func texel(i, size vec2) vec4 {
	return imageSrc3UnsafeAtFromSrc0Pos(imageSrc0Origin() + mod(i, size) + 0.5)
}

// encodeNormal packs a unit vector into 24 bits: its octahedral
// projection with 12 bits per coordinate.
func encodeNormal(n vec3) vec4 {
	n /= abs(n.x) + abs(n.y) + abs(n.z)
	e := n.xy
	if n.z < 0 {
		e = (1 - abs(n.yx)) * (step(0, n.xy)*2 - 1)
	}
	e = floor(clamp(e*0.5+0.5, 0, 1)*4095 + 0.5)
	h := floor(e / 16)
	l := e - h*16
	return vec4(h.x, h.y, l.x*16+l.y, 255) / 255
}
`

var maxBlend = ebiten.Blend{
	BlendFactorSourceRGB:        ebiten.BlendFactorOne,
	BlendFactorSourceAlpha:      ebiten.BlendFactorOne,
	BlendFactorDestinationRGB:   ebiten.BlendFactorOne,
	BlendFactorDestinationAlpha: ebiten.BlendFactorOne,
	BlendOperationRGB:           ebiten.BlendOperationMax,
	BlendOperationAlpha:         ebiten.BlendOperationMax,
}

// camera is the orbit's perspective projection onto a w×h pixel viewport.
type camera struct {
	eye             step.Vec3
	right, up, back step.Vec3 // view basis; the camera looks along -back
	w, h            int
	focal           float64 // pixels per unit of tan(angle off axis)
}

func (o *orbit) camera(w, h int) camera {
	right, up, back := o.basis()
	return camera{
		eye: o.eye(), right: right, up: up, back: back,
		w: w, h: h,
		focal: float64(h) / 2 / math.Tan(o.fov/2*math.Pi/180),
	}
}

// viewMatrix returns the map from an instance's local coordinates to view
// coordinates (x right, y up, z the distance in front of the camera), as
// rows of a 3×4 matrix.
func (c *camera) viewMatrix(world step.Affine) [3][4]float32 {
	var m [3][4]float32
	for r, axis := range [3]step.Vec3{c.right, c.up, c.back.Scale(-1)} {
		for j := range 3 {
			m[r][j] = float32(axis.X*world.R[0][j] + axis.Y*world.R[1][j] + axis.Z*world.R[2][j])
		}
		m[r][3] = float32(axis.Dot(world.T.Sub(c.eye)))
	}
	return m
}

// ray returns the picking ray through a viewport pixel.
func (c *camera) ray(px, py float64) (origin, dir step.Vec3) {
	x := (px - float64(c.w)/2) / c.focal
	y := (float64(c.h)/2 - py) / c.focal
	return c.eye, c.right.Scale(x).Add(c.up.Scale(y)).Sub(c.back).Norm()
}

type renderer struct {
	depth  [3]*ebiten.Image // depth bytes; in high quality mode later reused
	color  *ebiten.Image    // the finished picture
	shader *ebiten.Shader
	hq     hqPasses

	// The triangles that survive culling and their vertices, rebuilt every
	// frame, with scratch space indexed like the document's vertices and
	// the concatenation of its instances' indices.
	verts   []ebiten.Vertex
	indices []uint32
	view    []float32 // view position of each document vertex
	remap   []int32
	kept    []uint32
	idxOff  []int // per instance: its offset in kept
	doc     *document
	work    []instanceWork
	order   []int // visible instances (indices into work), grouped by texture

	// The document's textures on the GPU, each instance's index into them
	// (or -1), and the runs of the output that share a texture.
	textures []gpuTexture
	instTex  []int
	ranges   []drawRange

	// The inverse depth encoding of the last frame: 1/z = enc*depthA + depthB.
	depthA, depthB float32
}

// gpuTexture is a mesh texture uploaded for drawing.
type gpuTexture struct {
	img    *ebiten.Image
	cutout bool
}

// drawRange is a run of the projected vertices and triangles drawn with
// one texture (or none, -1). Its indices count from vStart.
type drawRange struct {
	vStart, vEnd, iStart, iEnd int
	tex                        int
}

// instanceWork holds per-frame results for one visible instance.
type instanceWork struct {
	near, far  float32
	used, kept int             // vertices and indices surviving culling
	extraV     []ebiten.Vertex // vertices made by clipping at the near plane
	extraI     []uint32        // triangles using them, numbered as in kept but with extraFlag for extraV
	vOff, iOff int             // offsets in the output
	vBase      int             // the start of the instance's draw range
}

// extraFlag marks an index into instanceWork.extraV.
const extraFlag = 1 << 31

// renderOptions selects how a frame is drawn.
type renderOptions struct {
	hq bool
	// supersample is the high quality mode's resolution factor (1 or 2);
	// at 1 the picture is smoothed with FXAA instead.
	supersample int
	// aoRadius is the reach of ambient occlusion in model units.
	aoRadius float32
	// grainSize is the size of the film grain in output pixels.
	grainSize       float32
	bgTop, bgBottom [3]float32 // background gradient (high quality mode)
}

// fitImage makes *img a w×h image, reusing it when it already is.
func fitImage(img **ebiten.Image, w, h int) {
	if *img != nil && (*img).Bounds().Dx() == w && (*img).Bounds().Dy() == h {
		return
	}
	if *img != nil {
		(*img).Deallocate()
	}
	*img = ebiten.NewImageWithOptions(image.Rect(0, 0, w, h), &ebiten.NewImageOptions{Unmanaged: true})
}

// render draws the document's visible triangles into r.color, a c.w×c.h
// image.
func (r *renderer) render(d *document, c *camera, opt renderOptions) {
	if r.shader == nil {
		s, err := ebiten.NewShader([]byte(geometryShaderSource))
		if err != nil {
			panic(err)
		}
		r.shader = s
	}
	ss := 1
	if opt.hq {
		ss = max(1, opt.supersample)
	}
	gc := *c
	gc.w, gc.h, gc.focal = c.w*ss, c.h*ss, c.focal*float64(ss)
	for i := range r.depth {
		fitImage(&r.depth[i], gc.w, gc.h)
		r.depth[i].Clear()
	}
	fitImage(&r.color, c.w, c.h)
	r.color.Clear()
	verts, idx := r.project(d, &gc)
	if len(idx) > 0 {
		r.drawDepth(verts)
	}
	if !opt.hq {
		if len(idx) > 0 {
			r.drawSurface(verts, &gc, r.color, 0)
		}
		return
	}
	// The colour and material go where the top depth byte was, and the
	// normal where the middle one was; the third depth image has the
	// whole depth.
	r.depth[0].Clear()
	r.depth[1].Clear()
	if len(idx) > 0 {
		r.drawSurface(verts, &gc, r.depth[0], 1)
		r.drawSurface(verts, &gc, r.depth[1], 2)
	}
	r.hq.render(r, c, &gc, ss, opt)
}

// drawDepth runs the three depth passes.
func (r *renderer) drawDepth(verts []ebiten.Vertex) {
	op := &ebiten.DrawTrianglesShaderOptions{Blend: maxBlend}
	for stage, dst := range r.depth {
		op.Uniforms = map[string]any{"Stage": stage}
		r.drawRanges(dst, verts, op)
		op.Images[stage] = dst
	}
}

// drawSurface draws an output of the visible fragments into dst.
func (r *renderer) drawSurface(verts []ebiten.Vertex, c *camera, dst *ebiten.Image, output int) {
	op := &ebiten.DrawTrianglesShaderOptions{Blend: ebiten.BlendCopy}
	if output == 0 {
		op.Blend = ebiten.BlendSourceOver
	}
	op.Images[0] = r.depth[2]
	op.Uniforms = map[string]any{
		"Stage":  3,
		"Output": output,
		"Right":  vec3Uniform(c.right),
		"Up":     vec3Uniform(c.up),
		"Back":   vec3Uniform(c.back),
	}
	r.drawRanges(dst, verts, op)
}

// drawRanges draws the projected triangles into dst one texture at a time,
// adding the texture uniforms to op's.
func (r *renderer) drawRanges(dst *ebiten.Image, verts []ebiten.Vertex, op *ebiten.DrawTrianglesShaderOptions) {
	op.Uniforms["DepthA"] = r.depthA
	op.Uniforms["DepthB"] = r.depthB
	for _, rg := range r.ranges {
		textured, cutout := 0, 0
		op.Images[3] = nil
		if rg.tex >= 0 {
			t := r.textures[rg.tex]
			op.Images[3] = t.img
			textured = 1
			if t.cutout {
				cutout = 1
			}
		}
		op.Uniforms["Textured"] = textured
		op.Uniforms["Cutout"] = cutout
		dst.DrawTrianglesShader32(verts[rg.vStart:rg.vEnd], r.indices[rg.iStart:rg.iEnd], r.shader, op)
	}
}

func vec3Uniform(v step.Vec3) []float32 {
	return []float32{float32(v.X), float32(v.Y), float32(v.Z)}
}

// prepare sizes the scratch space for a document.
func (r *renderer) prepare(d *document) {
	if r.doc == d {
		return
	}
	r.doc = d
	r.idxOff = make([]int, len(d.insts))
	n := 0
	for i, in := range d.insts {
		r.idxOff[i] = n
		n += len(in.mesh.Indices)
	}
	r.verts = make([]ebiten.Vertex, len(d.verts))
	r.indices = make([]uint32, n)
	r.view = make([]float32, len(d.verts)*3)
	r.remap = make([]int32, len(d.verts))
	r.kept = make([]uint32, n)

	for _, t := range r.textures {
		t.img.Deallocate()
	}
	r.textures = r.textures[:0]
	r.instTex = make([]int, len(d.insts))
	slots := map[*step.Texture]int{}
	for i, in := range d.insts {
		r.instTex[i] = -1
		if !textured(in.mesh) {
			continue
		}
		t := in.mesh.Texture
		slot, ok := slots[t]
		if !ok {
			slot = len(r.textures)
			slots[t] = slot
			r.textures = append(r.textures, gpuTexture{img: uploadTexture(texturePixels(t.Image, t.Cutout)), cutout: t.Cutout})
		}
		r.instTex[i] = slot
	}
}

// texturePixels converts a texture image to premultiplied RGBA for the
// GPU. The alpha of a texture without holes means nothing (glTF's opaque
// mode, or atlas padding), so it is made opaque, keeping the colour stored
// under transparent pixels instead of premultiplying it away.
func texturePixels(src image.Image, cutout bool) *image.RGBA {
	b := src.Bounds()
	r := image.Rect(0, 0, b.Dx(), b.Dy())
	rgba := image.NewRGBA(r)
	if cutout {
		draw.Draw(rgba, r, src, b.Min, draw.Src)
		return rgba
	}
	n := image.NewNRGBA(r)
	draw.Draw(n, r, src, b.Min, draw.Src)
	for i := 3; i < len(n.Pix); i += 4 {
		n.Pix[i] = 255
	}
	draw.Draw(rgba, r, n, image.Point{}, draw.Src)
	return rgba
}

// uploadTexture copies premultiplied pixels to the GPU.
func uploadTexture(px *image.RGBA) *ebiten.Image {
	img := ebiten.NewImageWithOptions(px.Bounds(), &ebiten.NewImageOptions{Unmanaged: true})
	img.WritePixels(px.Pix)
	return img
}

// project computes the screen position and depth of the visible
// instances' vertices, and returns the vertices and triangles to draw: those
// facing the camera, on screen and in front of the near plane.
func (r *renderer) project(d *document, c *camera) ([]ebiten.Vertex, []uint32) {
	if len(d.visible) == 0 {
		return nil, nil
	}
	r.prepare(d)
	m := c.viewMatrix(step.Identity())
	cx, cy, f := float32(c.w)/2, float32(c.h)/2, float32(c.focal)
	w, h := float32(c.w), float32(c.h)
	pos, verts, view := d.pos, d.verts, r.view
	r.work = slices.Grow(r.work[:0], len(d.visible))[:len(d.visible)]

	// Screen and view positions (x, y and depth), and texture coordinates
	// divided by depth.
	parallelEach(len(d.visible), func(k int) {
		in := &d.insts[d.visible[k]]
		textured := r.instTex[d.visible[k]] >= 0
		mn, mx := float32(math.Inf(1)), float32(math.Inf(-1))
		for i := in.vert0; i < in.vert0+len(in.mesh.Positions)/3; i++ {
			x, y, z := pos[i*3], pos[i*3+1], pos[i*3+2]
			vx := m[0][0]*x + m[0][1]*y + m[0][2]*z + m[0][3]
			vy := m[1][0]*x + m[1][1]*y + m[1][2]*z + m[1][3]
			vz := m[2][0]*x + m[2][1]*y + m[2][2]*z + m[2][3]
			v := &verts[i]
			view[i*3], view[i*3+1], view[i*3+2] = vx, vy, vz
			if vz > 0 {
				v.DstX = cx + f*vx/vz
				v.DstY = cy - f*vy/vz
				if textured {
					v.SrcX, v.SrcY = d.uv[i*2]/vz, d.uv[i*2+1]/vz
				}
			}
			mn = min(mn, vz)
			mx = max(mx, vz)
		}
		r.work[k] = instanceWork{near: mn, far: mx}
	})
	near, far := float32(math.Inf(1)), float32(math.Inf(-1))
	for _, wk := range r.work {
		near, far = min(near, wk.near), max(far, wk.far)
	}
	if far <= 0 {
		return nil, nil
	}
	// Geometry closer than clipNear is cut away; inverse depth is spread
	// over the remaining range.
	clipNear := far * 1e-3
	near = max(near, clipNear)
	if far-near < far*1e-6 {
		far = near * (1 + 1e-6)
	}
	scale := 1 / (1/near - 1/far)
	offset := 1 / far
	encode := func(z float32) float32 { return (1/z - offset) * scale }
	r.depthA, r.depthB = 1/scale, offset
	// facing reports whether a screen triangle faces the camera and touches
	// the screen. Front faces are counter-clockwise seen from outside, so
	// clockwise on screen, where y points down.
	facing := func(va, vb, vc *ebiten.Vertex) bool {
		return (vb.DstX-va.DstX)*(vc.DstY-va.DstY)-(vb.DstY-va.DstY)*(vc.DstX-va.DstX) < 0 &&
			max(va.DstX, vb.DstX, vc.DstX) >= 0 && min(va.DstX, vb.DstX, vc.DstX) <= w &&
			max(va.DstY, vb.DstY, vc.DstY) >= 0 && min(va.DstY, vb.DstY, vc.DstY) <= h
	}

	// Encode the depths and cull the triangles, numbering the vertices that
	// are still needed.
	parallelEach(len(d.visible), func(k int) {
		ii := d.visible[k]
		in := &d.insts[ii]
		mesh := in.mesh
		base := in.vert0
		iv := verts[base : base+len(mesh.Positions)/3]
		ivw := view[base*3 : (base+len(iv))*3]
		var iuv []float32
		if r.instTex[ii] >= 0 {
			iuv = d.uv[base*2 : (base+len(iv))*2]
		}
		for i := range iv {
			if z := ivw[i*3+2]; z >= clipNear {
				iv[i].Custom0 = encode(z)
			}
		}
		remap := r.remap[base : base+len(iv)]
		for i := range remap {
			remap[i] = -1
		}
		wk := &r.work[k]
		use := func(x uint32) {
			if remap[x] < 0 {
				remap[x] = int32(wk.used)
				wk.used++
			}
		}
		kept := r.kept[r.idxOff[ii]:]
		for t := 0; t+2 < len(mesh.Indices); t += 3 {
			tri := [3]uint32{mesh.Indices[t], mesh.Indices[t+1], mesh.Indices[t+2]}
			va, vb, vc := &iv[tri[0]], &iv[tri[1]], &iv[tri[2]]
			if ivw[tri[0]*3+2] < clipNear || ivw[tri[1]*3+2] < clipNear || ivw[tri[2]*3+2] < clipNear {
				wk.clip(tri, iv, ivw, iuv, clipNear, func(x, y float32) (float32, float32) {
					return cx + f*x/clipNear, cy - f*y/clipNear
				}, encode(clipNear), facing, use)
				continue
			}
			if !facing(va, vb, vc) {
				continue
			}
			for _, x := range tri {
				use(x)
			}
			copy(kept[wk.kept:], tri[:])
			wk.kept += 3
		}
	})

	// Pack the survivors, grouped by texture so that each texture's
	// triangles and vertices form one range.
	r.order = r.order[:0]
	for k := range r.work {
		r.order = append(r.order, k)
	}
	slices.SortStableFunc(r.order, func(a, b int) int {
		return r.instTex[d.visible[a]] - r.instTex[d.visible[b]]
	})
	r.ranges = r.ranges[:0]
	nv, ni := 0, 0
	for _, k := range r.order {
		wk := &r.work[k]
		tex := r.instTex[d.visible[k]]
		if n := len(r.ranges); n == 0 || r.ranges[n-1].tex != tex {
			r.ranges = append(r.ranges, drawRange{vStart: nv, iStart: ni, tex: tex})
		}
		rg := &r.ranges[len(r.ranges)-1]
		wk.vOff, wk.iOff, wk.vBase = nv, ni, rg.vStart
		nv += wk.used + len(wk.extraV)
		ni += wk.kept + len(wk.extraI)
		rg.vEnd, rg.iEnd = nv, ni
	}
	parallelEach(len(d.visible), func(k int) {
		ii := d.visible[k]
		in := &d.insts[ii]
		wk := &r.work[k]
		base := in.vert0
		remap := r.remap[base : base+len(in.mesh.Positions)/3]
		for i, j := range remap {
			if j >= 0 {
				r.verts[wk.vOff+int(j)] = verts[base+i]
			}
		}
		copy(r.verts[wk.vOff+wk.used:], wk.extraV)
		out := r.indices[wk.iOff:]
		off := wk.vOff - wk.vBase
		for t, x := range r.kept[r.idxOff[ii] : r.idxOff[ii]+wk.kept] {
			out[t] = uint32(off + int(remap[x]))
		}
		out = out[wk.kept:]
		for t, x := range wk.extraI {
			if x&extraFlag != 0 {
				out[t] = uint32(off + wk.used + int(x&^extraFlag))
			} else {
				out[t] = uint32(off + int(remap[x]))
			}
		}
	})
	return r.verts[:nv], r.indices[:ni]
}

// clip cuts a triangle crossing the near plane at depth zn and keeps the
// part in front: a triangle or a quadrilateral. iv and ivw are the
// instance's vertices and their view positions, and iuv their texture
// coordinates if it is textured. New vertices interpolate the surface
// attributes along the cut edges.
func (wk *instanceWork) clip(tri [3]uint32, iv []ebiten.Vertex, ivw, iuv []float32, zn float32,
	project func(x, y float32) (float32, float32), depth float32,
	facing func(a, b, c *ebiten.Vertex) bool, use func(uint32)) {
	type corner struct {
		v   ebiten.Vertex
		ref uint32 // index into iv, or extraFlag for a new vertex
	}
	var poly [4]corner
	n := 0
	for k := range 3 {
		a, b := tri[k], tri[(k+1)%3]
		za, zb := ivw[a*3+2], ivw[b*3+2]
		if za >= zn {
			poly[n] = corner{iv[a], a}
			n++
		}
		if (za >= zn) != (zb >= zn) {
			t := (zn - za) / (zb - za)
			lerp := func(a, b float32) float32 { return a + (b-a)*t }
			i, j := &iv[a], &iv[b]
			v := ebiten.Vertex{
				ColorR: lerp(i.ColorR, j.ColorR), ColorG: lerp(i.ColorG, j.ColorG), ColorB: lerp(i.ColorB, j.ColorB), ColorA: i.ColorA,
				Custom0: depth,
				Custom1: lerp(i.Custom1, j.Custom1), Custom2: lerp(i.Custom2, j.Custom2), Custom3: lerp(i.Custom3, j.Custom3),
			}
			v.DstX, v.DstY = project(lerp(ivw[a*3], ivw[b*3]), lerp(ivw[a*3+1], ivw[b*3+1]))
			if iuv != nil {
				v.SrcX, v.SrcY = lerp(iuv[a*2], iuv[b*2])/zn, lerp(iuv[a*2+1], iuv[b*2+1])/zn
			}
			poly[n] = corner{v, extraFlag}
			n++
		}
	}
	add := func(c *corner) uint32 {
		switch {
		case c.ref&extraFlag == 0:
			use(c.ref)
		case c.ref == extraFlag: // not yet added
			c.ref |= uint32(len(wk.extraV))
			wk.extraV = append(wk.extraV, c.v)
		}
		return c.ref
	}
	for k := 1; k+1 < n; k++ {
		a, b, c := &poly[0], &poly[k], &poly[k+1]
		if facing(&a.v, &b.v, &c.v) {
			wk.extraI = append(wk.extraI, add(a), add(b), add(c))
		}
	}
}

// parallelFor calls f on contiguous sub-ranges of [0, n), concurrently.
func parallelFor(n int, f func(lo, hi int)) {
	chunks := min(runtime.GOMAXPROCS(0), max(1, n/4096))
	per := (n + chunks - 1) / chunks
	var wg sync.WaitGroup
	for lo := 0; lo < n; lo += per {
		hi := min(n, lo+per)
		wg.Go(func() {
			f(lo, hi)
		})
	}
	wg.Wait()
}

// parallelEach calls f for each of [0, n), on all cores; suited to items of
// very different cost.
func parallelEach(n int, f func(i int)) {
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), n) {
		wg.Go(func() {
			for {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				f(i)
			}
		})
	}
	wg.Wait()
}
