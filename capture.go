package main

import (
	"image"
	"image/png"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/guigui-gui/guigui"
	"github.com/hajimehoshi/ebiten/v2"
)

// captureOverlay is a debugging aid: when STEPVIEW_CAPTURE names a PNG
// file, the window contents are saved there shortly after a model has
// loaded and the program exits.
type captureOverlay struct {
	guigui.DefaultWidget

	path    string
	armed   bool
	waiting int
	done    bool
}

func (c *captureOverlay) arm(ticks int) {
	if c.path == "" || c.armed {
		return
	}
	c.armed = true
	c.waiting = ticks
}

func (c *captureOverlay) Tick(context *guigui.Context, widgetBounds *guigui.WidgetBounds) error {
	if c.done {
		return ebiten.Termination
	}
	if c.armed && c.waiting > 0 {
		c.waiting--
		if c.waiting == 0 {
			guigui.RequestRedraw(c)
		}
	}
	return nil
}

func (c *captureOverlay) Draw(context *guigui.Context, widgetBounds *guigui.WidgetBounds, dst *ebiten.Image) {
	if !c.armed || c.waiting > 0 || c.done {
		return
	}
	b := dst.Bounds()
	img := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	dst.ReadPixels(img.Pix)
	f, err := os.Create(c.path)
	if err != nil {
		log.Print(err)
	} else {
		if err := png.Encode(f, img); err != nil {
			log.Print(err)
		}
		f.Close()
	}
	c.done = true
}

// benchmark spins the camera for a number of ticks and reports the frame
// rate (enabled with STEPVIEW_BENCH, together with STEPVIEW_CAPTURE).
type benchmark struct {
	ticks   int
	minFPS  float64
	sumFPS  float64
	samples int
}

func (b *benchmark) tick(v *view3D) bool {
	b.ticks++
	v.orbit.yaw += 0.02
	v.cameraMoved()
	if b.ticks > 30 {
		fps := ebiten.ActualFPS()
		if b.samples == 0 || fps < b.minFPS {
			b.minFPS = fps
		}
		b.sumFPS += fps
		b.samples++
	}
	if b.ticks >= 240 {
		log.Printf("benchmark: average %.1f FPS, min %.1f FPS (%d triangles)", b.sumFPS/float64(b.samples), b.minFPS, v.doc.visibleTris)
		return true
	}
	return false
}

// runScript performs debugging actions from STEPVIEW_SCRIPT, a comma
// separated list of "pick" (click the centre of the view), "hide:N" (hide
// tree node N), "center:N" (as if node N were double-clicked), "select:N",
// "zoom:F" (scale the camera distance), "wire" (switch to wireframe),
// "hq" (switch to high quality), "spin" (start spinning) and "sidebar"
// (toggle the sidebar).
func (r *Root) runScript(context *guigui.Context, script string) {
	for act := range strings.SplitSeq(script, ",") {
		switch {
		case act == "pick":
			c := r.view.viewSize.Div(2)
			if o, d, ok := r.view.ray(c); ok {
				node := r.doc.pick(o, d)
				log.Printf("script: picked node %d (%s)", node, nodeName(r.doc, node))
				r.handlePick(node)
			}
		case strings.HasPrefix(act, "zoom:"):
			f, _ := strconv.ParseFloat(strings.TrimPrefix(act, "zoom:"), 64)
			r.view.orbit.dist *= f
			r.view.requestRender()
		case strings.HasPrefix(act, "select:"):
			n, _ := strconv.Atoi(strings.TrimPrefix(act, "select:"))
			r.handlePick(n)
		case act == "wire":
			r.view.setMode(modeWireframe)
		case act == "hq":
			r.view.setMode(modeHighQuality)
		case act == "spin":
			r.view.setSpinning(true)
		case act == "sidebar":
			r.sidebarHidden = !r.sidebarHidden
		case strings.HasPrefix(act, "center:"):
			n, _ := strconv.Atoi(strings.TrimPrefix(act, "center:"))
			r.onDouble(context, n)
			log.Printf("script: centred node %d (%s)", n, nodeName(r.doc, n))
		case strings.HasPrefix(act, "hide:"):
			n, _ := strconv.Atoi(strings.TrimPrefix(act, "hide:"))
			r.doc.setHidden(n, true)
			log.Printf("script: hid node %d (%s)", n, nodeName(r.doc, n))
		}
	}
	guigui.RequestRebuild()
}

func nodeName(d *document, n int) string {
	if n < 0 || n >= len(d.nodes) {
		return "none"
	}
	return d.nodes[n].name
}
