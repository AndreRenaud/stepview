package main

import (
	"errors"
	"fmt"
	"image"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/guigui-gui/guigui"
	"github.com/guigui-gui/guigui/basicwidget"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"github.com/sqweek/dialog"

	"github.com/AndreRenaud/stepview/internal/gltfload"
	"github.com/AndreRenaud/stepview/internal/step"
)

type loadResult struct {
	path string
	doc  *document
	err  error
	dur  time.Duration
}

// loadModel reads a STEP or glTF file.
func loadModel(path string, progress func(string, float64)) (*step.Model, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".gltf", ".glb":
		return gltfload.LoadFile(path)
	}
	opt := step.DefaultOptions()
	opt.Progress = progress
	return step.LoadFile(path, opt)
}

// Root is the application window.
type Root struct {
	guigui.DefaultWidget

	background  basicwidget.Background
	openButton  basicwidget.Button
	fitButton   basicwidget.Button
	isoButton   basicwidget.Button
	topButton   basicwidget.Button
	frontButton basicwidget.Button
	rightButton basicwidget.Button
	showButton  basicwidget.Button
	wireButton  basicwidget.Button
	status      basicwidget.Text
	tree        basicwidget.List[int]
	rows        guigui.WidgetSlice[*treeRow]
	splitter    splitter
	view        view3D
	placeholder basicwidget.Text
	capture     captureOverlay
	bench       *benchmark

	script        string
	scriptPending bool
	scriptDelay   int

	// pendingPath is loaded once nothing else is loading. It comes from
	// the command line or from openDocs (Finder).
	pendingPath string
	openDocs    <-chan string
	started     bool

	loadCh   chan loadResult
	dialogCh chan string
	loading  bool

	progressMu   sync.Mutex
	progressText string

	statusText string
	doc        *document
	docSerial  int
	revealNode int // node to reveal in the tree on the next build, or -1
	revealSet  bool
	treeWidth  int

	treeItems    []basicwidget.ListItem[int]
	toolbarItems []guigui.LinearLayoutItem
	bodyItems    []guigui.LinearLayoutItem
	mainItems    []guigui.LinearLayoutItem
	onVis        func(context *guigui.Context, node int, visible bool)
	onDouble     func(context *guigui.Context, node int)
}

func (r *Root) WriteStateKey(context *guigui.Context, w *guigui.StateKeyWriter) {
	w.WriteString(r.statusText)
	w.WriteInt(r.docSerial)
	w.WriteBool(r.loading)
	w.WriteInt(r.treeWidth)
	if r.doc != nil {
		w.WriteInt(r.doc.gen)
	}
}

func (r *Root) setStatus(s string) {
	r.statusText = s
}

func (r *Root) startLoad(path string) {
	if r.loading {
		return
	}
	if r.loadCh == nil {
		r.loadCh = make(chan loadResult, 1)
	}
	r.loading = true
	r.setStatus("Loading " + filepath.Base(path) + "…")
	go func() {
		t0 := time.Now()
		m, err := loadModel(path, func(stage string, f float64) {
			r.progressMu.Lock()
			r.progressText = fmt.Sprintf("Loading %s… %s %d%%", filepath.Base(path), stage, int(f*100))
			r.progressMu.Unlock()
		})
		var d *document
		if err == nil {
			r.progressMu.Lock()
			r.progressText = fmt.Sprintf("Loading %s… preparing display", filepath.Base(path))
			r.progressMu.Unlock()
			d = newDocument(path, m)
		}
		r.loadCh <- loadResult{path: path, doc: d, err: err, dur: time.Since(t0)}
	}()
}

func (r *Root) openDialog() {
	if r.dialogCh == nil {
		r.dialogCh = make(chan string, 1)
	}
	go func() {
		// sqweek/dialog runs the native panel on the main thread itself; we
		// only wait for the answer here.
		path, err := dialog.File().
			Title("Open model").
			Filter("STEP files", "step", "stp", "p21").
			Filter("glTF files", "gltf", "glb").
			Load()
		if err != nil {
			if !errors.Is(err, dialog.ErrCancelled) {
				r.dialogCh <- ""
			}
			return
		}
		r.dialogCh <- path
	}()
}

func (r *Root) Tick(context *guigui.Context, widgetBounds *guigui.WidgetBounds) error {
	if !r.started {
		r.started = true
		r.revealNode = -1
	}
	select {
	case path := <-r.openDocs:
		r.pendingPath = path
	default:
	}
	if r.pendingPath != "" && !r.loading {
		r.startLoad(r.pendingPath)
		r.pendingPath = ""
	}
	select {
	case path := <-r.dialogCh:
		if path != "" {
			r.startLoad(path)
		}
	default:
	}
	select {
	case res := <-r.loadCh:
		r.loading = false
		if res.err != nil {
			r.setStatus(fmt.Sprintf("Failed to load %s: %v", filepath.Base(res.path), res.err))
			msg := fmt.Sprintf("Could not load %s:\n%v", res.path, res.err)
			go dialog.Message("%s", msg).Title("Load failed").Error()
		} else {
			r.doc = res.doc
			r.docSerial++
			r.view.setDocument(r.doc)
			r.rows.SetLen(0)
			s := r.doc.model.Stats
			extra := ""
			if s.FailedFaces > 0 {
				extra = fmt.Sprintf(", %d faces failed", s.FailedFaces)
			}
			r.setStatus(fmt.Sprintf("%s — %d parts, %s triangles%s (%.1fs)",
				filepath.Base(res.path), len(r.doc.insts), humanCount(r.doc.triangles), extra, res.dur.Seconds()))
			context.SetWindowTitle(filepath.Base(res.path) + " — STEP Viewer")
			if r.bench == nil {
				r.capture.arm(30)
			}
			r.scriptPending = r.script != ""
			r.scriptDelay = 10
		}
	default:
	}
	if r.scriptPending && r.doc != nil {
		if r.scriptDelay--; r.scriptDelay <= 0 {
			r.scriptPending = false
			r.runScript(context, r.script)
		}
	}
	if r.bench != nil && r.doc != nil && !r.loading {
		if r.bench.tick(&r.view) {
			r.bench = nil
			r.capture.arm(2)
		}
	}
	if r.loading {
		r.progressMu.Lock()
		p := r.progressText
		r.progressMu.Unlock()
		if p != "" && p != r.statusText {
			r.setStatus(p)
		}
	}
	return nil
}

func humanCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprint(n)
}

func (r *Root) selectNode(node int) {
	if r.doc == nil {
		return
	}
	r.doc.setSelected(node)
}

// handlePick selects a node picked in the 3D view and reveals it in the
// tree.
func (r *Root) handlePick(node int) {
	if r.doc == nil {
		return
	}
	r.selectNode(node)
	if node >= 0 {
		// Expand the ancestors so the item can be shown.
		for p := r.doc.nodes[node].parent; p >= 0; p = r.doc.nodes[p].parent {
			r.doc.nodes[p].collapsed = false
		}
	}
	r.revealNode = node
	r.revealSet = true
}

func (r *Root) Build(context *guigui.Context, adder *guigui.ChildAdder) error {
	adder.AddWidget(&r.background)
	adder.AddWidget(&r.openButton)
	adder.AddWidget(&r.fitButton)
	adder.AddWidget(&r.isoButton)
	adder.AddWidget(&r.topButton)
	adder.AddWidget(&r.frontButton)
	adder.AddWidget(&r.rightButton)
	adder.AddWidget(&r.showButton)
	adder.AddWidget(&r.wireButton)
	adder.AddWidget(&r.status)
	adder.AddWidget(&r.tree)
	adder.AddWidget(&r.splitter)
	adder.AddWidget(&r.view)
	if r.doc == nil {
		adder.AddWidget(&r.placeholder)
	}
	if r.capture.path != "" {
		adder.AddWidget(&r.capture)
	}

	r.openButton.SetText("Open…")
	r.openButton.OnDown(func(context *guigui.Context) { r.openDialog() })
	context.SetEnabled(&r.openButton, !r.loading)

	hasDoc := r.doc != nil
	r.fitButton.SetText("Fit")
	r.fitButton.OnDown(func(context *guigui.Context) { r.view.fit(-1) })
	r.isoButton.SetText("Iso")
	r.isoButton.OnDown(func(context *guigui.Context) { r.view.setView(-60*math.Pi/180, 30*math.Pi/180) })
	r.topButton.SetText("Top")
	r.topButton.OnDown(func(context *guigui.Context) { r.view.setView(-math.Pi/2, math.Pi/2-1e-3) })
	r.frontButton.SetText("Front")
	r.frontButton.OnDown(func(context *guigui.Context) { r.view.setView(-math.Pi/2, 0) })
	r.rightButton.SetText("Right")
	r.rightButton.OnDown(func(context *guigui.Context) { r.view.setView(0, 0) })
	r.showButton.SetText("Show all")
	r.showButton.OnDown(func(context *guigui.Context) {
		if r.doc != nil {
			r.doc.showAll()
		}
	})
	r.wireButton.SetText("Wireframe")
	r.wireButton.SetToggleable(true)
	r.wireButton.SetPressed(r.view.wireframe)
	r.wireButton.OnDown(func(context *guigui.Context) { r.view.setWireframe(!r.view.wireframe) })
	for _, b := range []*basicwidget.Button{&r.fitButton, &r.isoButton, &r.topButton, &r.frontButton, &r.rightButton, &r.showButton, &r.wireButton} {
		context.SetEnabled(b, hasDoc)
	}

	r.status.SetValue(r.statusText)
	r.status.SetVerticalAlign(basicwidget.VerticalAlignMiddle)
	r.status.SetEllipsisString("…")

	if r.loading {
		r.placeholder.SetValue("Loading…")
	} else {
		r.placeholder.SetValue("Open a STEP file to view it")
	}
	r.placeholder.SetHorizontalAlign(basicwidget.HorizontalAlignCenter)
	r.placeholder.SetVerticalAlign(basicwidget.VerticalAlignMiddle)

	r.splitter.OnMoved(func(context *guigui.Context, dx int) {
		r.treeWidth = max(basicwidget.UnitSize(context)*4, r.currentTreeWidth(context)+dx)
	})

	r.view.OnPicked(func(context *guigui.Context, node int) {
		r.handlePick(node)
	})

	// Tree.
	r.tree.SetStripeVisible(false)
	r.tree.OnItemSelected(func(context *guigui.Context, index int) {
		if r.doc == nil || index < 0 || index >= len(r.doc.nodes) {
			return
		}
		r.selectNode(index)
	})
	r.tree.OnItemExpanderToggled(func(context *guigui.Context, index int, expanded bool) {
		if r.doc == nil || index < 0 || index >= len(r.doc.nodes) {
			return
		}
		r.doc.nodes[index].collapsed = !expanded
	})
	if r.onVis == nil {
		r.onVis = func(context *guigui.Context, node int, visible bool) {
			if r.doc != nil {
				r.doc.setHidden(node, !visible)
			}
		}
	}
	if r.onDouble == nil {
		r.onDouble = func(context *guigui.Context, node int) {
			r.view.centerOn(node)
		}
	}
	r.treeItems = slices.Delete(r.treeItems, 0, len(r.treeItems))
	if r.doc != nil {
		r.rows.SetLen(len(r.doc.nodes))
		for i := range r.doc.nodes {
			n := &r.doc.nodes[i]
			row := r.rows.At(i)
			row.set(i, n.name, !n.hidden)
			row.OnVisibilityChanged(r.onVis)
			row.OnDoubleClicked(r.onDouble)
			r.treeItems = append(r.treeItems, basicwidget.ListItem[int]{
				Content:     row,
				Value:       i,
				IndentLevel: n.depth + 1,
				Collapsed:   n.collapsed,
			})
		}
	} else {
		r.rows.SetLen(0)
	}
	r.tree.SetItems(r.treeItems)
	if r.revealSet {
		r.revealSet = false
		if r.revealNode >= 0 {
			r.tree.SelectItemByIndex(r.revealNode)
			r.tree.EnsureItemVisibleByIndex(r.revealNode)
		} else {
			r.tree.SelectItemByIndex(-1)
		}
	}
	return nil
}

func (r *Root) currentTreeWidth(context *guigui.Context) int {
	if r.treeWidth > 0 {
		return r.treeWidth
	}
	return basicwidget.UnitSize(context) * 13
}

func (r *Root) Layout(context *guigui.Context, widgetBounds *guigui.WidgetBounds, layouter *guigui.ChildLayouter) {
	u := basicwidget.UnitSize(context)
	b := widgetBounds.Bounds()
	layouter.LayoutWidget(&r.background, b)
	layouter.LayoutWidget(&r.capture, b)

	r.toolbarItems = slices.Delete(r.toolbarItems, 0, len(r.toolbarItems))
	r.toolbarItems = append(r.toolbarItems,
		guigui.LinearLayoutItem{Widget: &r.openButton},
		guigui.LinearLayoutItem{Size: guigui.FixedSize(u / 4)},
		guigui.LinearLayoutItem{Widget: &r.fitButton},
		guigui.LinearLayoutItem{Widget: &r.isoButton},
		guigui.LinearLayoutItem{Widget: &r.topButton},
		guigui.LinearLayoutItem{Widget: &r.frontButton},
		guigui.LinearLayoutItem{Widget: &r.rightButton},
		guigui.LinearLayoutItem{Size: guigui.FixedSize(u / 4)},
		guigui.LinearLayoutItem{Widget: &r.showButton},
		guigui.LinearLayoutItem{Widget: &r.wireButton},
		guigui.LinearLayoutItem{Size: guigui.FixedSize(u / 2)},
		guigui.LinearLayoutItem{Widget: &r.status, Size: guigui.FlexibleSize(1)},
	)
	tw := min(r.currentTreeWidth(context), b.Dx()-u*6)
	r.bodyItems = slices.Delete(r.bodyItems, 0, len(r.bodyItems))
	r.bodyItems = append(r.bodyItems,
		guigui.LinearLayoutItem{Widget: &r.tree, Size: guigui.FixedSize(tw)},
		guigui.LinearLayoutItem{Widget: &r.splitter, Size: guigui.FixedSize(u / 3)},
		guigui.LinearLayoutItem{Widget: &r.view, Size: guigui.FlexibleSize(1)},
	)
	r.mainItems = slices.Delete(r.mainItems, 0, len(r.mainItems))
	r.mainItems = append(r.mainItems,
		guigui.LinearLayoutItem{
			Size: guigui.FixedSize(u * 3 / 2),
			Layout: guigui.LinearLayout{
				Direction: guigui.LayoutDirectionHorizontal,
				Items:     r.toolbarItems,
				Gap:       u / 4,
				Padding:   guigui.Padding{Start: u / 4, End: u / 4, Top: u / 4},
			},
		},
		guigui.LinearLayoutItem{
			Size: guigui.FlexibleSize(1),
			Layout: guigui.LinearLayout{
				Direction: guigui.LayoutDirectionHorizontal,
				Items:     r.bodyItems,
				Padding:   guigui.Padding{Start: u / 4, Top: u / 4, Bottom: u / 4},
			},
		},
	)
	(guigui.LinearLayout{
		Direction: guigui.LayoutDirectionVertical,
		Items:     r.mainItems,
	}).LayoutWidgets(context, b, layouter)

	if r.doc == nil {
		// The placeholder covers the 3D view.
		vb := image.Rect(b.Min.X+tw+u/4+u/3, b.Min.Y+u*3/2+u/4, b.Max.X, b.Max.Y-u/4)
		layouter.LayoutWidget(&r.placeholder, vb)
	}
}

// showAll makes every node visible.
func (d *document) showAll() {
	changed := false
	for i := range d.nodes {
		if d.nodes[i].hidden {
			d.nodes[i].hidden = false
			changed = true
		}
	}
	if !changed {
		return
	}
	d.updateVisible()
	d.gen++
}

var splitterEventMoved = guigui.GenerateEventKey()

// splitter is a draggable divider between the tree and the view.
type splitter struct {
	guigui.DefaultWidget

	dragging bool
	lastX    int
	hovered  bool
}

func (s *splitter) OnMoved(f func(context *guigui.Context, dx int)) {
	guigui.SetEventHandler(s, splitterEventMoved, f)
}

func (s *splitter) HandlePointingInput(context *guigui.Context, widgetBounds *guigui.WidgetBounds) guigui.HandleInputResult {
	x, _ := ebiten.CursorPosition()
	if !s.dragging {
		if widgetBounds.IsHitAtCursor() && inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
			s.dragging = true
			s.lastX = x
			return guigui.HandleInputByWidget(s)
		}
		return guigui.HandleInputResult{}
	}
	if !ebiten.IsMouseButtonPressed(ebiten.MouseButtonLeft) {
		s.dragging = false
		return guigui.HandleInputByWidget(s)
	}
	if dx := x - s.lastX; dx != 0 {
		s.lastX = x
		guigui.DispatchEvent(s, splitterEventMoved, dx)
		return guigui.HandleInputByWidget(s)
	}
	return guigui.AbortHandlingInputByWidget(s)
}

func (s *splitter) Tick(context *guigui.Context, widgetBounds *guigui.WidgetBounds) error {
	if h := widgetBounds.IsHitAtCursor(); h != s.hovered {
		s.hovered = h
		guigui.RequestRedraw(s)
	}
	return nil
}

func (s *splitter) CursorShape(context *guigui.Context, widgetBounds *guigui.WidgetBounds) (ebiten.CursorShapeType, bool) {
	return ebiten.CursorShapeEWResize, true
}

func (s *splitter) Draw(context *guigui.Context, widgetBounds *guigui.WidgetBounds, dst *ebiten.Image) {
	b := widgetBounds.Bounds()
	if !s.hovered && !s.dragging {
		return
	}
	x := float32(b.Min.X+b.Max.X) / 2
	sc := float32(context.Scale())
	clr := basicwidget.ListItemColorTypeHovered.BackgroundColor(context)
	if clr == nil {
		return
	}
	vector.StrokeLine(dst, x, float32(b.Min.Y), x, float32(b.Max.Y), 2*sc, clr, true)
}
