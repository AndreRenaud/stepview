package main

import (
	"github.com/hajimehoshi/ebiten/v2"
)

// The high quality renderer shades from a G-buffer, built by the geometry
// passes in render.go at the supersampled resolution:
//
//   - depth[0]: surface colour (sRGB) and material code (see material.go)
//   - depth[1]: view-space normal, octahedral with 12 bits per coordinate
//   - depth[2]: inverse depth, low, high and middle bytes; alpha marks
//     geometry
//
// Screen-space passes then compute ambient occlusion at the output
// resolution and blur it, light every surface with a physically based
// model, blend in the transparent surfaces (lit as they are drawn, and
// tested against depth[2]), and reduce the result to the output, either by
// averaging 2×2 samples or with FXAA, adding film grain on the way.
//
// View space here has x right, y up and z towards the viewer.

// hqCommon holds declarations shared by the high quality shaders.
const hqCommon = `
// SS is the supersampling factor: G-buffer pixels per output pixel.
var SS float

// Center and Focal describe the projection, in G-buffer pixels.
var Center vec2
var Focal float

// Inverse depth is enc*DepthA + DepthB.
var DepthA float
var DepthB float

// viewZ returns the distance in front of the camera stored in a texel of
// the depth image.
func viewZ(d vec4) float {
	enc := (d.g*65536 + d.b*256 + d.r) * 255 / 16777215
	return 1 / (enc*DepthA + DepthB)
}

// viewPos returns the view-space position of G-buffer pixel centre p at
// distance z.
func viewPos(p vec2, z float) vec3 {
	return vec3((p.x-Center.x)/Focal*z, (Center.y-p.y)/Focal*z, -z)
}

// decodeNormal undoes encodeNormal in render.go.
func decodeNormal(c vec4) vec3 {
	c = floor(c*255 + 0.5)
	lx := floor(c.b / 16)
	ly := c.b - lx*16
	e := (vec2(c.r, c.g)*16+vec2(lx, ly))/4095*2 - 1
	n := vec3(e, 1-abs(e.x)-abs(e.y))
	if n.z < 0 {
		f := (1 - abs(n.yx)) * (step(0, n.xy)*2 - 1)
		n = vec3(f, n.z)
	}
	return normalize(n)
}
`

// ssaoShaderSource estimates ambient occlusion by testing points in the
// hemisphere around each surface's normal against the depth image
// (src0), with the normals in src1.
const ssaoShaderSource = `//kage:unit pixels

package main

// Radius is the reach of the occlusion in model units, and MaxRadius its
// limit in G-buffer pixels.
var Radius float
var MaxRadius float
` + hqCommon + `
func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	p := floor(dstPos.xy - imageDstOrigin())
	g := p*SS + 0.5
	dc := imageSrc0At(g + imageSrc0Origin())
	if dc.a == 0 {
		return vec4(1)
	}
	z := viewZ(dc)
	n := decodeNormal(imageSrc1At(g + imageSrc1Origin()))
	r := min(Radius, MaxRadius*z/Focal)
	pos := viewPos(g, z) + n*(r*0.02)

	// A per-pixel rotation of the sample pattern (interleaved gradient
	// noise), which the blur pass averages out.
	noise := fract(52.9829189 * fract(dot(p, vec2(0.06711056, 0.00583715))))
	ref := vec3(0, 1, 0)
	if abs(n.y) > 0.9 {
		ref = vec3(1, 0, 0)
	}
	t := normalize(cross(ref, n))
	b := cross(n, t)

	occ := 0.0
	for i := 0; i < 16; i++ {
		fi := float(i)
		// Cosine-weighted directions on a spiral, kept off the tangent
		// plane, at distances favouring the near field.
		u := (fi + 0.5) / 16 * 0.85
		phi := fi*2.39996323 + noise*6.2831853
		h := vec3(sqrt(u)*cos(phi), sqrt(u)*sin(phi), sqrt(1-u))
		l := fract(fi*0.618034 + noise)
		s := pos + (t*h.x+b*h.y+n*h.z)*(r*mix(0.1, 1, l*l))
		sz := -s.z
		if sz <= 0 {
			continue
		}
		q := floor(vec2(Center.x+Focal*s.x/sz, Center.y-Focal*s.y/sz)) + 0.5
		ds := imageSrc0At(q + imageSrc0Origin())
		if ds.a > 0 {
			zs := viewZ(ds)
			if zs < sz-r*0.02 {
				occ += clamp(r/abs(z-zs), 0, 1)
			}
		}
	}
	ao := 1 - occ/16
	return vec4(ao, ao, ao, 1)
}
`

// blurShaderSource smooths the occlusion (src0) over 4×4 output pixels,
// leaving out samples at a different depth (src1).
const blurShaderSource = `//kage:unit pixels

package main

// Radius is the occlusion radius; depths further apart than that are not
// mixed.
var Radius float
` + hqCommon + `
func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	p := floor(dstPos.xy - imageDstOrigin())
	dc := imageSrc1At(p*SS + 0.5 + imageSrc1Origin())
	if dc.a == 0 {
		return vec4(1)
	}
	z := viewZ(dc)
	sum := 0.0
	wsum := 0.0
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			q := p + vec2(float(x)-1.5, float(y)-1.5)
			ds := imageSrc1At(floor(q)*SS + 0.5 + imageSrc1Origin())
			if ds.a > 0 {
				w := max(0, 1-abs(viewZ(ds)-z)/Radius)
				sum += w * imageSrc0At(floor(q)+0.5+imageSrc0Origin()).r
				wsum += w
			}
		}
	}
	ao := 1.0
	if wsum > 0 {
		ao = sum / wsum
	}
	return vec4(ao, ao, ao, 1)
}
`

// compositeShaderSource lights the G-buffer: colour and material in src0,
// normals in src1 and the blurred occlusion in src2. Three lights move
// with the camera like a photographer's rig; the surroundings are a
// studio fixed to the model, so reflections move as the model turns.
const compositeShaderSource = `//kage:unit pixels

package main

// WX, WY and WZ are the model axes in view space.
var WX vec3
var WY vec3
var WZ vec3

// BgTop and BgBottom are the background gradient's colours, and Height
// the G-buffer height.
var BgTop vec3
var BgBottom vec3
var Height float
` + hqCommon + `
const pi = 3.14159265

func toModel(v vec3) vec3 {
	return vec3(dot(v, WX), dot(v, WY), dot(v, WZ))
}

// softbox returns the light from a rectangular soft box centred in
// direction c, with axes t and b and half sizes hs (on the plane at unit
// distance), seen in direction w by a surface of the given roughness. Its
// edges blur with roughness while its total output stays the same.
func softbox(w, c, t, b vec3, hs vec2, rough float) float {
	d := dot(w, c)
	if d <= 0.05 {
		return 0
	}
	uv := abs(vec2(dot(w, t), dot(w, b))) / d
	e := 0.01 + rough*rough*1.5
	inside := (1 - smoothstep(hs.x-e, hs.x+e, uv.x)) * (1 - smoothstep(hs.y-e, hs.y+e, uv.y))
	return inside * hs.x * hs.y / ((hs.x + e) * (hs.y + e))
}

// The studio, in model coordinates (Z up): a pale grey cyclorama over a
// light floor that darkens away from the horizon, a large soft box
// overhead and two strip lights.
const nadirL = 0.38
const horizonL = 0.55
const zenithL = 0.7
const overheadL = 1.5

func studio(w vec3, rough float) vec3 {
	e := 0.03 + rough*0.4
	sky := mix(horizonL, zenithL, smoothstep(0, 1, w.z))
	ground := mix(horizonL*0.9, nadirL, smoothstep(0, 0.7, -w.z))
	l := mix(ground, sky, smoothstep(-e, e, w.z))
	// Pale panels and dark flags around the walls, which give upright
	// metal something to reflect; blurred away on rough surfaces, and
	// averaging out in studioIrradiance.
	az := atan2(w.y, w.x)
	walls := sin(3*az+0.6)*0.6 + sin(5*az+2.1)*0.4
	l *= 1 + walls*0.4*(1-rough)*(1-smoothstep(0.2, 0.7, abs(w.z)))
	l += overheadL * softbox(w, vec3(0, 0, 1), vec3(1, 0, 0), vec3(0, 1, 0), vec2(0.7, 0.35), rough)
	// The strip lights are brighter in reflections than their share of
	// studioIrradiance, as a photographer's reflectors would make them,
	// to give metals crisp highlights.
	l += 4.5 * softbox(w, normalize(vec3(1, -0.3, 0.3)), normalize(vec3(0.3, 1, 0)), normalize(vec3(-0.3, 0.09, 1)), vec2(0.2, 0.6), rough)
	l += 4 * softbox(w, normalize(vec3(-0.6, 0.8, 0.25)), normalize(vec3(-0.8, -0.6, 0)), normalize(vec3(0.15, -0.2, 1)), vec2(0.2, 0.6), rough)
	return vec3(l) * vec3(1, 0.99, 0.97)
}

// studioIrradiance is the studio's light on a matte surface facing n,
// divided by pi.
func studioIrradiance(n vec3) vec3 {
	sky := mix((nadirL+horizonL)*0.5, (horizonL+zenithL)*0.5, 0.5+0.5*n.z)
	l := sky + overheadL*0.98*max(n.z, 0)/pi
	l += 3 * 0.29 * max(dot(n, normalize(vec3(1, -0.3, 0.3))), 0) / pi
	l += 2.5 * 0.29 * max(dot(n, normalize(vec3(-0.6, 0.8, 0.25))), 0) / pi
	return vec3(l) * vec3(1, 0.99, 0.97)
}

// envBRDF is Karis's fit of the split-sum environment BRDF.
func envBRDF(f0 vec3, rough, ndv float) vec3 {
	r := rough*vec4(-1, -0.0275, -0.572, 0.022) + vec4(1, 0.0425, 1.04, -0.04)
	a := min(r.x*r.x, exp2(-9.28*ndv))*r.x + r.y
	ab := vec2(-1.04, 1.04)*a + r.zw
	return f0*ab.x + ab.y
}

// direct returns the light reflected towards v from a directional light
// l of unit intensity: GGX specular and Lambert diffuse.
func direct(n, v, l, base, f0 vec3, metal, rough float) vec3 {
	ndl := dot(n, l)
	if ndl <= 0 {
		return vec3(0)
	}
	h := normalize(l + v)
	ndv := max(dot(n, v), 1e-4)
	ndh := max(dot(n, h), 0)
	a := max(rough, 0.08)
	a *= a
	a2 := a * a
	dd := ndh*ndh*(a2-1) + 1
	d := a2 / (pi * dd * dd)
	k := a / 2
	g := ndl / (ndl*(1-k) + k) * ndv / (ndv*(1-k) + k)
	f := f0 + (1-f0)*pow(1-max(dot(v, h), 0), 5)
	spec := d * g * f / (4 * ndl * ndv + 1e-4)
	diff := (1 - f) * (1 - metal) * base / pi
	return (diff + spec) * ndl
}

// neutral is the Khronos PBR Neutral tone mapper, which keeps base colours
// faithful.
func neutral(c vec3) vec3 {
	x := min(c.r, min(c.g, c.b))
	off := 0.04
	if x < 0.08 {
		off = x - 6.25*x*x
	}
	c -= off
	peak := max(c.r, max(c.g, c.b))
	if peak < 0.76 {
		return c
	}
	d := 1 - 0.76
	np := 1 - d*d/(peak+d-0.76)
	c *= np / peak
	g := 1 - 1/(0.15*(peak-np)+1)
	return mix(c, vec3(np), g)
}

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	p := floor(dstPos.xy-imageDstOrigin()) + 0.5
	a := imageSrc0At(p + imageSrc0Origin())
	if a.a == 0 {
		return vec4(mix(BgTop, BgBottom, p.y/Height), 1)
	}
	n := decodeNormal(imageSrc1At(p + imageSrc1Origin()))
	ao := imageSrc2At(floor((p-0.5)/SS) + 0.5 + imageSrc2Origin()).r

	code := floor(a.a*255 + 0.5)
	metal := step(127.5, code)
	rough := clamp((code-metal*128)/127, 0.05, 1)
	base := pow(a.rgb, vec3(2.2))
	f0 := mix(vec3(0.04), base, metal)

	v := -normalize(vec3((p.x-Center.x)/Focal, (Center.y-p.y)/Focal, -1))
	// Interpolated normals can turn slightly away near silhouettes.
	if dot(n, v) < 0.02 {
		n = normalize(n + v*(0.02-dot(n, v)))
	}
	ndv := max(dot(n, v), 1e-4)

	// The camera's lights: key from the upper left, fill from the right
	// and a rim light from behind.
	lit := direct(n, v, normalize(vec3(-0.45, 0.55, 0.7)), base, f0, metal, rough) * vec3(1.45, 1.4, 1.3)
	lit += direct(n, v, normalize(vec3(0.7, 0.05, 0.7)), base, f0, metal, rough) * 0.4
	lit += direct(n, v, normalize(vec3(0.2, 0.7, -0.7)), base, f0, metal, rough) * 0.8
	lit *= mix(1, ao, 0.5)

	// The studio.
	diff := base * (1 - metal) * studioIrradiance(toModel(n)) * ao
	// Occlusion dims reflections less on metals, which mirror their
	// surroundings rather than scatter light.
	specOcc := mix(1, clamp(pow(ndv+ao, exp2(-16*rough-1))-1+ao, 0, 1), mix(0.6, 0.3, metal))
	spec := studio(toModel(reflect(-v, n)), rough) * envBRDF(f0, rough, ndv) * specOcc

	c := neutral((lit + diff + spec) * 0.72)
	return vec4(pow(clamp(c, 0, 1), vec3(1/2.2)), 1)
}
`

// hqGrain adds film grain to the output: soft monochrome noise, strongest
// in the mid-tones, with a new pattern on every frame.
const hqGrain = `
// GrainAmount is the grain's peak-to-peak strength, GrainSize its size in
// output pixels and GrainSeed picks the pattern.
var GrainAmount float
var GrainSize float
var GrainSeed float

// hash returns a pseudo-random number in [0, 1) for a lattice point
// (Hoskins's hash without sine).
func hash(p vec2) float {
	q := fract(vec3(p.x, p.y, p.x) * 0.1031)
	q += dot(q, q.yzx+33.33)
	return fract((q.x + q.y) * q.z)
}

func grain(c vec3, p vec2) vec3 {
	g := p/GrainSize + vec2(GrainSeed*37, GrainSeed*17)
	i := floor(g)
	f := g - i
	f = f * f * (3 - 2*f)
	n := mix(mix(hash(i), hash(i+vec2(1, 0)), f.x), mix(hash(i+vec2(0, 1)), hash(i+vec2(1, 1)), f.x), f.y)
	l := dot(c, vec3(0.299, 0.587, 0.114))
	return clamp(c+(n-0.5)*GrainAmount*(0.4+2.4*l*(1-l)), 0, 1)
}
`

// resolveShaderSource averages 2×2 samples of src0 into each output pixel
// and adds grain.
const resolveShaderSource = `//kage:unit pixels

package main
` + hqGrain + `
func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	p := dstPos.xy - imageDstOrigin()
	q := floor(p)*2 + 0.5 + imageSrc0Origin()
	c := imageSrc0At(q) + imageSrc0At(q+vec2(1, 0)) + imageSrc0At(q+vec2(0, 1)) + imageSrc0At(q+vec2(1, 1))
	return vec4(grain(c.rgb/4, p), 1)
}
`

// fxaaShaderSource smooths jagged edges in src0 (FXAA, after Lottes) and
// adds grain.
const fxaaShaderSource = `//kage:unit pixels

package main
` + hqGrain + `

// sample reads src0 at p (in pixels) with bilinear filtering.
func sample(p vec2) vec3 {
	p = clamp(p, vec2(0.5), imageSrc0Size()-0.5) - 0.5
	i := floor(p)
	f := p - i
	o := imageSrc0Origin() + i + 0.5
	c00 := imageSrc0At(o).rgb
	c10 := imageSrc0At(o + vec2(1, 0)).rgb
	c01 := imageSrc0At(o + vec2(0, 1)).rgb
	c11 := imageSrc0At(o + vec2(1, 1)).rgb
	return mix(mix(c00, c10, f.x), mix(c01, c11, f.x), f.y)
}

func luma(c vec3) float {
	return dot(c, vec3(0.299, 0.587, 0.114))
}

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	p := dstPos.xy - imageDstOrigin()
	m := sample(p)
	lnw := luma(sample(p + vec2(-1, -1)))
	lne := luma(sample(p + vec2(1, -1)))
	lsw := luma(sample(p + vec2(-1, 1)))
	lse := luma(sample(p + vec2(1, 1)))
	lm := luma(m)
	lmin := min(lm, min(min(lnw, lne), min(lsw, lse)))
	lmax := max(lm, max(max(lnw, lne), max(lsw, lse)))
	dir := vec2(-((lnw + lne) - (lsw + lse)), (lnw+lsw)-(lne+lse))
	reduce := max((lnw+lne+lsw+lse)*(0.25/8), 1.0/128)
	dir = clamp(dir/(min(abs(dir.x), abs(dir.y))+reduce), vec2(-8), vec2(8))
	a := 0.5 * (sample(p+dir*(1.0/3-0.5)) + sample(p+dir*(2.0/3-0.5)))
	b := a*0.5 + 0.25*(sample(p-dir*0.5)+sample(p+dir*0.5))
	lb := luma(b)
	if lb < lmin || lb > lmax {
		return vec4(grain(a, p), 1)
	}
	return vec4(grain(b, p), 1)
}
`

// hqPasses holds the high quality renderer's screen-space passes and
// their images.
type hqPasses struct {
	ssao, blur, composite, fxaa, resolve *ebiten.Shader
	ao, aoBlur                           *ebiten.Image
	lit                                  *ebiten.Image // the shaded G-buffer
	frame                                int           // counts renders, to vary the grain
}

// grainAmount is the film grain's peak-to-peak strength in the high
// quality renderer.
const grainAmount = 0.08

func mustShader(src string) *ebiten.Shader {
	s, err := ebiten.NewShader([]byte(src))
	if err != nil {
		panic(err)
	}
	return s
}

// render shades the G-buffer in r.depth, made with camera gc at ss times
// the resolution of c, into r.color.
func (h *hqPasses) render(r *renderer, c, gc *camera, ss int, opt renderOptions) {
	if h.ssao == nil {
		h.ssao = mustShader(ssaoShaderSource)
		h.blur = mustShader(blurShaderSource)
		h.composite = mustShader(compositeShaderSource)
		h.fxaa = mustShader(fxaaShaderSource)
		h.resolve = mustShader(resolveShaderSource)
	}
	h.frame++
	fitImage(&h.ao, c.w, c.h)
	fitImage(&h.aoBlur, c.w, c.h)
	fitImage(&h.lit, gc.w, gc.h)
	right, up, back := gc.right, gc.up, gc.back
	u := map[string]any{
		"SS":        float32(ss),
		"Center":    []float32{float32(gc.w) / 2, float32(gc.h) / 2},
		"Focal":     float32(gc.focal),
		"DepthA":    r.depthA,
		"DepthB":    r.depthB,
		"Radius":    opt.aoRadius,
		"MaxRadius": float32(gc.h) * 0.08,
		"WX":        []float32{float32(right.X), float32(up.X), float32(back.X)},
		"WY":        []float32{float32(right.Y), float32(up.Y), float32(back.Y)},
		"WZ":        []float32{float32(right.Z), float32(up.Z), float32(back.Z)},
		"BgTop":     opt.bgTop[:],
		"BgBottom":  opt.bgBottom[:],
		"Height":    float32(gc.h),
	}
	fullscreen(h.ao, h.ssao, u, r.depth[2], r.depth[1])
	fullscreen(h.aoBlur, h.blur, u, h.ao, r.depth[2])
	fullscreen(h.lit, h.composite, u, r.depth[0], r.depth[1], h.aoBlur)
	// Transparent surfaces are lit simply, as they are drawn, and blended
	// in before the picture is reduced.
	r.drawTransparent(gc, h.lit)
	g := map[string]any{
		"GrainAmount": float32(grainAmount),
		"GrainSize":   max(1, opt.grainSize),
		"GrainSeed":   float32(h.frame % 97),
	}
	if ss == 1 {
		fullscreen(r.color, h.fxaa, g, h.lit)
	} else {
		fullscreen(r.color, h.resolve, g, h.lit)
	}
}

// fullscreen covers dst with a shader's output.
func fullscreen(dst *ebiten.Image, s *ebiten.Shader, uniforms map[string]any, srcs ...*ebiten.Image) {
	b := dst.Bounds()
	x0, y0, x1, y1 := float32(b.Min.X), float32(b.Min.Y), float32(b.Max.X), float32(b.Max.Y)
	vs := []ebiten.Vertex{
		{DstX: x0, DstY: y0, SrcX: x0, SrcY: y0},
		{DstX: x1, DstY: y0, SrcX: x1, SrcY: y0},
		{DstX: x0, DstY: y1, SrcX: x0, SrcY: y1},
		{DstX: x1, DstY: y1, SrcX: x1, SrcY: y1},
	}
	op := &ebiten.DrawTrianglesShaderOptions{Blend: ebiten.BlendCopy, Uniforms: uniforms}
	copy(op.Images[:], srcs)
	fillImages(op.Images[:])
	dst.DrawTrianglesShader32(vs, []uint32{0, 1, 2, 1, 3, 2}, s, op)
}
