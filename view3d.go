package main

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"time"

	"github.com/guigui-gui/guigui"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"

	"github.com/AndreRenaud/stepview/internal/step"
)

var view3DEventPicked = guigui.GenerateEventKey()

// renderMode is how the 3D view draws the model.
type renderMode int

const (
	modeNormal renderMode = iota
	modeWireframe
	modeHighQuality
)

// settleTicks is how long the camera must rest before the high quality
// renderer draws at full quality.
const settleTicks = 12

// spinPeriod is the time of one turn in spin mode.
const spinPeriod = 24 * time.Second

// maxSupersampledPixels limits the view size at which the high quality
// renderer supersamples, to bound its memory and time.
const maxSupersampledPixels = 5_000_000

// orbit is a turntable camera around a target point, in model coordinates
// (Z up).
type orbit struct {
	target     step.Vec3
	dist       float64
	yaw, pitch float64
	fov        float64 // vertical field of view in degrees
}

func defaultOrbit() orbit {
	return orbit{dist: 100, yaw: -60 * math.Pi / 180, pitch: 30 * math.Pi / 180, fov: 35}
}

// back returns the unit vector from the target towards the eye.
func (o *orbit) back() step.Vec3 {
	cp := math.Cos(o.pitch)
	return step.Vec3{X: cp * math.Cos(o.yaw), Y: cp * math.Sin(o.yaw), Z: math.Sin(o.pitch)}
}

func (o *orbit) basis() (right, up, back step.Vec3) {
	back = o.back()
	right = step.Vec3{Z: 1}.Cross(back).Norm()
	if right.Len() < 0.5 {
		right = step.Vec3{X: -math.Sin(o.yaw), Y: math.Cos(o.yaw)}
	}
	up = back.Cross(right)
	return
}

func (o *orbit) eye() step.Vec3 { return o.target.Add(o.back().Scale(o.dist)) }

// view3D renders the document and handles camera interaction.
type view3D struct {
	guigui.DefaultWidget

	doc         *document
	renderedDoc *document
	renderedGen int
	needRender  bool

	renderer renderer
	size     image.Point // size of the last render
	wire     *ebiten.Image

	mode       renderMode
	lastShaded renderMode // the mode to return to from wireframe
	white      *ebiten.Image

	// ticks counts frames; lastMove is the tick of the last camera move,
	// and coarse is set when the last high quality render was not at full
	// quality.
	ticks, lastMove int
	coarse          bool
	renderedDark    bool // the colour mode of the last render's background

	spinning bool
	lastSpin time.Time // when the spin last advanced, or zero

	orbit orbit

	dragButton ebiten.MouseButton
	dragging   bool
	moved      bool
	pressPos   image.Point
	lastPos    image.Point
	viewSize   image.Point

	// The last pick's position and camera, to recognise clicking the same
	// spot again, which picks the next object behind.
	pickPos   image.Point
	pickOrbit orbit
	pickSize  image.Point
	picked    bool
}

// OnPicked registers a handler called with the picked node (or -1) when
// the user clicks in the view.
func (v *view3D) OnPicked(f func(context *guigui.Context, node int)) {
	guigui.SetEventHandler(v, view3DEventPicked, f)
}

// setMode changes the render mode.
func (v *view3D) setMode(m renderMode) {
	if v.mode == m {
		return
	}
	if m == modeWireframe {
		v.lastShaded = v.mode
	}
	v.mode = m
	v.requestRender()
}

// toggleWireframe switches between wireframe and the last shaded mode.
func (v *view3D) toggleWireframe() {
	if v.mode == modeWireframe {
		v.setMode(v.lastShaded)
	} else {
		v.setMode(modeWireframe)
	}
}

// setSpinning starts or stops turning the camera around the model.
func (v *view3D) setSpinning(on bool) {
	v.spinning = on
	v.lastSpin = time.Time{}
}

// centerOn pans the camera so a node's geometry is centred in the view,
// keeping the zoom and orientation.
func (v *view3D) centerOn(node int) {
	if v.doc == nil {
		return
	}
	b := v.doc.subtreeBounds(node)
	if b.Empty() {
		return
	}
	v.orbit.target = b.Center()
	v.requestRender()
}

func (v *view3D) WriteStateKey(context *guigui.Context, w *guigui.StateKeyWriter) {
	if v.doc != nil {
		w.WriteInt(v.doc.gen)
		w.WriteInt(len(v.doc.nodes))
	}
	w.WriteBool(v.doc != nil)
	w.WriteInt(int(v.mode))
}

// setDocument shows a new document and frames it.
func (v *view3D) setDocument(d *document) {
	if d == v.doc {
		return
	}
	v.doc = d
	v.picked = false
	if d == nil {
		return
	}
	v.orbit = defaultOrbit()
	if s := os.Getenv("STEPVIEW_VIEW"); s != "" {
		// Debugging aid: "yaw,pitch" in degrees.
		var yaw, pitch float64
		if _, err := fmt.Sscanf(s, "%g,%g", &yaw, &pitch); err == nil {
			v.orbit.yaw, v.orbit.pitch = yaw*math.Pi/180, pitch*math.Pi/180
		}
	}
	v.fit(-1)
}

// fit frames the visible geometry (or a subtree when sub >= 0).
func (v *view3D) fit(sub int) {
	if v.doc == nil {
		return
	}
	b := v.doc.visibleBounds(sub)
	if b.Empty() {
		b = v.doc.bounds
	}
	if b.Empty() {
		return
	}
	r := math.Max(b.Diag()/2, 1e-6)
	v.orbit.target = b.Center()
	half := v.orbit.fov / 2 * math.Pi / 180
	if v.viewSize.X > 0 && v.viewSize.Y > 0 && v.viewSize.X < v.viewSize.Y {
		half = math.Atan(math.Tan(half) * float64(v.viewSize.X) / float64(v.viewSize.Y))
	}
	v.orbit.dist = r / math.Sin(half) * 1.05
	v.requestRender()
}

// setView sets a standard view direction.
func (v *view3D) setView(yaw, pitch float64) {
	v.orbit.yaw, v.orbit.pitch = yaw, pitch
	v.requestRender()
}

func (v *view3D) requestRender() {
	v.needRender = true
	guigui.RequestRedraw(v)
}

// cameraMoved redraws after an interactive camera change, which the high
// quality renderer draws quickly until the camera rests.
func (v *view3D) cameraMoved() {
	v.lastMove = v.ticks
	v.requestRender()
}

func (v *view3D) Tick(context *guigui.Context, widgetBounds *guigui.WidgetBounds) error {
	v.ticks++
	if v.spinning && v.doc != nil {
		now := time.Now()
		if !v.lastSpin.IsZero() {
			// Limit the step so that a stall does not jump the view.
			dt := min(now.Sub(v.lastSpin), 100*time.Millisecond)
			v.orbit.yaw -= 2 * math.Pi * dt.Seconds() / spinPeriod.Seconds()
			v.orbit.yaw = math.Remainder(v.orbit.yaw, 2*math.Pi)
		}
		v.lastSpin = now
		v.cameraMoved()
	}
	if v.coarse && !v.dragging && v.ticks-v.lastMove > settleTicks {
		v.requestRender()
	}
	return nil
}

// backgroundColours returns the view's background gradient.
func backgroundColours(dark bool) (top, bottom [3]float32) {
	if dark {
		return [3]float32{0.22, 0.23, 0.26}, [3]float32{0.09, 0.09, 0.11}
	}
	return [3]float32{0.86, 0.88, 0.92}, [3]float32{0.55, 0.58, 0.64}
}

func (v *view3D) render(context *guigui.Context, size image.Point) {
	c := v.orbit.camera(size.X, size.Y)
	v.coarse = false
	v.renderedDark = context.ColorMode() == ebiten.ColorModeDark
	switch v.mode {
	case modeNormal:
		v.renderer.render(v.doc, &c, renderOptions{})
	case modeHighQuality:
		opt := renderOptions{
			hq:          true,
			supersample: 1,
			aoRadius:    float32(v.doc.bounds.Diag() * 0.012),
			grainSize:   float32(context.Scale()),
		}
		opt.bgTop, opt.bgBottom = backgroundColours(v.renderedDark)
		if size.X*size.Y <= maxSupersampledPixels {
			if v.dragging || v.ticks-v.lastMove <= settleTicks {
				v.coarse = true
			} else {
				opt.supersample = 2
			}
		}
		v.renderer.render(v.doc, &c, opt)
	case modeWireframe:
		if v.wire == nil || v.wire.Bounds().Size() != size {
			if v.wire != nil {
				v.wire.Deallocate()
			}
			v.wire = ebiten.NewImage(size.X, size.Y)
		}
		v.wire.Clear()
		v.renderWireframe(v.wire, &c, float32(1.2*context.Scale()), context.ColorMode() == ebiten.ColorModeDark)
	}
	v.size = size
	v.needRender = false
	v.renderedDoc = v.doc
	v.renderedGen = v.doc.gen
}

// ray returns a picking ray in model coordinates for a point in widget
// pixels.
func (v *view3D) ray(p image.Point) (step.Vec3, step.Vec3, bool) {
	if v.viewSize.X <= 0 || v.viewSize.Y <= 0 {
		return step.Vec3{}, step.Vec3{}, false
	}
	c := v.orbit.camera(v.viewSize.X, v.viewSize.Y)
	o, d := c.ray(float64(p.X)+0.5, float64(p.Y)+0.5)
	return o, d, true
}

func (v *view3D) HandlePointingInput(context *guigui.Context, widgetBounds *guigui.WidgetBounds) guigui.HandleInputResult {
	b := widgetBounds.Bounds()
	v.viewSize = b.Size()
	pos := image.Pt(ebiten.CursorPosition())
	if !v.dragging {
		if !widgetBounds.IsHitAtCursor() {
			return guigui.HandleInputResult{}
		}
		for _, btn := range []ebiten.MouseButton{ebiten.MouseButtonLeft, ebiten.MouseButtonRight, ebiten.MouseButtonMiddle} {
			if inpututil.IsMouseButtonJustPressed(btn) {
				v.dragging = true
				v.dragButton = btn
				v.pressPos = pos
				v.lastPos = pos
				v.moved = false
				context.SetFocused(v, true)
				return guigui.AbortHandlingInputByWidget(v)
			}
		}
		if _, wy := ebiten.Wheel(); wy != 0 && v.doc != nil {
			v.zoom(math.Pow(0.85, wy), pos.Sub(b.Min))
			v.cameraMoved()
			return guigui.AbortHandlingInputByWidget(v)
		}
		return guigui.HandleInputResult{}
	}
	if ebiten.IsMouseButtonPressed(v.dragButton) {
		threshold := 4 * context.Scale()
		if dp := pos.Sub(v.pressPos); math.Hypot(float64(dp.X), float64(dp.Y)) > threshold {
			v.moved = true
		}
		d := pos.Sub(v.lastPos)
		v.lastPos = pos
		if v.moved && (d.X != 0 || d.Y != 0) && v.doc != nil {
			s := 1 / context.Scale()
			switch v.dragButton {
			case ebiten.MouseButtonLeft:
				// Turning the model by hand takes over from spinning.
				v.spinning = false
				v.orbit.yaw -= float64(d.X) * 0.008 * s
				v.orbit.pitch += float64(d.Y) * 0.008 * s
				v.orbit.pitch = math.Max(-math.Pi/2+1e-3, math.Min(math.Pi/2-1e-3, v.orbit.pitch))
			default:
				right, up, _ := v.orbit.basis()
				h := float64(max(1, b.Dy()))
				perPixel := 2 * v.orbit.dist * math.Tan(v.orbit.fov/2*math.Pi/180) / h
				v.orbit.target = v.orbit.target.Sub(right.Scale(float64(d.X) * perPixel)).Add(up.Scale(float64(d.Y) * perPixel))
			}
			v.cameraMoved()
		}
		return guigui.AbortHandlingInputByWidget(v)
	}
	// Released.
	v.dragging = false
	if !v.moved && v.dragButton == ebiten.MouseButtonLeft && v.doc != nil {
		guigui.DispatchEvent(v, view3DEventPicked, v.pickAt(context, pos.Sub(b.Min)))
		return guigui.HandleInputByWidget(v)
	}
	return guigui.AbortHandlingInputByWidget(v)
}

// pickAt returns the node to select for a click at a point in widget
// pixels, or -1. Clicking the same spot again without moving the camera
// steps through the objects under it, from front to back.
func (v *view3D) pickAt(context *guigui.Context, p image.Point) int {
	o, d, ok := v.ray(p)
	if !ok {
		return -1
	}
	hits := v.doc.pickAll(o, d)
	dp := p.Sub(v.pickPos)
	slop := 4 * context.Scale()
	again := v.picked && v.pickOrbit == v.orbit && v.pickSize == v.viewSize &&
		math.Hypot(float64(dp.X), float64(dp.Y)) <= slop
	v.pickPos, v.pickOrbit, v.pickSize, v.picked = p, v.orbit, v.viewSize, true
	if len(hits) == 0 {
		return -1
	}
	if again {
		return v.doc.nextPick(hits)
	}
	return hits[0]
}

// zoom scales the camera distance, keeping the point under the cursor
// fixed on the focal plane.
func (v *view3D) zoom(factor float64, cursor image.Point) {
	if o, d, ok := v.ray(cursor); ok {
		_, _, back := v.orbit.basis()
		// Intersect the cursor ray with the plane through the target.
		den := d.Dot(back.Scale(-1))
		if den > 1e-6 {
			t := v.orbit.target.Sub(o).Dot(back.Scale(-1)) / den
			p := o.Add(d.Scale(t))
			v.orbit.target = v.orbit.target.Add(p.Sub(v.orbit.target).Scale(1 - factor))
		}
	}
	v.orbit.dist *= factor
	v.requestRender()
}

func (v *view3D) HandleButtonInput(context *guigui.Context, widgetBounds *guigui.WidgetBounds) guigui.HandleInputResult {
	if v.doc == nil {
		return guigui.HandleInputResult{}
	}
	switch {
	case inpututil.IsKeyJustPressed(ebiten.KeyF):
		v.fit(-1)
	case inpututil.IsKeyJustPressed(ebiten.KeyS):
		v.fit(v.doc.selected)
	case inpututil.IsKeyJustPressed(ebiten.KeyW):
		v.toggleWireframe()
	default:
		return guigui.HandleInputResult{}
	}
	return guigui.AbortHandlingInputByWidget(v)
}

func (v *view3D) CursorShape(context *guigui.Context, widgetBounds *guigui.WidgetBounds) (ebiten.CursorShapeType, bool) {
	if v.dragging && v.moved {
		if v.dragButton == ebiten.MouseButtonLeft {
			return ebiten.CursorShapeMove, true
		}
		return ebiten.CursorShapeMove, true
	}
	return ebiten.CursorShapeDefault, true
}

func (v *view3D) Draw(context *guigui.Context, widgetBounds *guigui.WidgetBounds, dst *ebiten.Image) {
	b := widgetBounds.Bounds()
	v.viewSize = b.Size()
	v.drawBackground(context, dst, b)
	if v.doc == nil || b.Dx() < 2 || b.Dy() < 2 {
		return
	}
	dark := context.ColorMode() == ebiten.ColorModeDark
	if v.needRender || v.size != b.Size() || v.renderedDoc != v.doc || v.renderedGen != v.doc.gen || v.renderedDark != dark {
		v.render(context, b.Size())
	}
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Translate(float64(b.Min.X), float64(b.Min.Y))
	if v.mode == modeWireframe {
		dst.DrawImage(v.wire, op)
	} else {
		dst.DrawImage(v.renderer.color, op)
	}
	v.drawAxes(context, dst, b)
}

func (v *view3D) drawBackground(context *guigui.Context, dst *ebiten.Image, b image.Rectangle) {
	if v.white == nil {
		v.white = ebiten.NewImage(3, 3)
		v.white.Fill(color.White)
	}
	top, bot := backgroundColours(context.ColorMode() == ebiten.ColorModeDark)
	x0, y0, x1, y1 := float32(b.Min.X), float32(b.Min.Y), float32(b.Max.X), float32(b.Max.Y)
	vs := []ebiten.Vertex{
		{DstX: x0, DstY: y0, SrcX: 1, SrcY: 1, ColorR: top[0], ColorG: top[1], ColorB: top[2], ColorA: 1},
		{DstX: x1, DstY: y0, SrcX: 1, SrcY: 1, ColorR: top[0], ColorG: top[1], ColorB: top[2], ColorA: 1},
		{DstX: x0, DstY: y1, SrcX: 1, SrcY: 1, ColorR: bot[0], ColorG: bot[1], ColorB: bot[2], ColorA: 1},
		{DstX: x1, DstY: y1, SrcX: 1, SrcY: 1, ColorR: bot[0], ColorG: bot[1], ColorB: bot[2], ColorA: 1},
	}
	dst.DrawTriangles(vs, []uint16{0, 1, 2, 1, 3, 2}, v.white, nil)
}

// drawAxes draws a small orientation triad in the bottom-left corner.
func (v *view3D) drawAxes(context *guigui.Context, dst *ebiten.Image, b image.Rectangle) {
	s := float32(context.Scale())
	right, up, back := v.orbit.basis()
	cx := float32(b.Min.X) + 40*s
	cy := float32(b.Max.Y) - 40*s
	axes := []struct {
		d step.Vec3
		c color.RGBA
	}{
		{step.Vec3{X: 1}, color.RGBA{220, 50, 50, 255}},
		{step.Vec3{Y: 1}, color.RGBA{50, 170, 50, 255}},
		{step.Vec3{Z: 1}, color.RGBA{60, 90, 230, 255}},
	}
	// Draw back-to-front so nearer axes overlap farther ones.
	order := []int{0, 1, 2}
	for i := range 3 {
		for j := i + 1; j < 3; j++ {
			if axes[order[j]].d.Dot(back) < axes[order[i]].d.Dot(back) {
				order[i], order[j] = order[j], order[i]
			}
		}
	}
	for _, k := range order {
		a := axes[k]
		x := cx + float32(a.d.Dot(right))*28*s
		y := cy - float32(a.d.Dot(up))*28*s
		vector.StrokeLine(dst, cx, cy, x, y, 2.5*s, a.c, true)
		vector.FillCircle(dst, x, y, 3.5*s, a.c, true)
	}
}
