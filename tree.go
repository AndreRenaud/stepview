package main

import (
	"image"
	"slices"
	"time"

	"github.com/guigui-gui/guigui"
	"github.com/guigui-gui/guigui/basicwidget"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

var (
	treeRowEventVisibility  = guigui.GenerateEventKey()
	treeRowEventDoubleClick = guigui.GenerateEventKey()
)

// doubleClickInterval is the maximum time between the clicks of a
// double-click.
const doubleClickInterval = 400 * time.Millisecond

// treeRow is the content of one tree item: a visibility checkbox and the
// node name.
type treeRow struct {
	guigui.DefaultWidget

	check basicwidget.Checkbox
	text  basicwidget.Text

	name    string
	visible bool
	node    int

	onCheck     func(context *guigui.Context, value bool)
	lastClick   time.Time
	clickPos    image.Point
	layoutItems []guigui.LinearLayoutItem
}

func (r *treeRow) set(node int, name string, visible bool) {
	r.node = node
	r.name = name
	r.visible = visible
}

// OnVisibilityChanged registers a handler for checkbox toggles.
func (r *treeRow) OnVisibilityChanged(f func(context *guigui.Context, node int, visible bool)) {
	guigui.SetEventHandler(r, treeRowEventVisibility, f)
}

// OnDoubleClicked registers a handler for double-clicks on the row.
func (r *treeRow) OnDoubleClicked(f func(context *guigui.Context, node int)) {
	guigui.SetEventHandler(r, treeRowEventDoubleClick, f)
}

// HandlePointingInput detects double-clicks. It never consumes the input,
// so the list still handles selection.
func (r *treeRow) HandlePointingInput(context *guigui.Context, widgetBounds *guigui.WidgetBounds) guigui.HandleInputResult {
	// The row's children are usually what is under the cursor, so test the
	// row's bounds rather than IsHitAtCursor.
	pos := image.Pt(ebiten.CursorPosition())
	if !pos.In(widgetBounds.VisibleBounds()) || !inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		return guigui.HandleInputResult{}
	}
	now := time.Now()
	d := pos.Sub(r.clickPos)
	slop := int(4 * context.Scale())
	if now.Sub(r.lastClick) <= doubleClickInterval && max(d.X, -d.X) <= slop && max(d.Y, -d.Y) <= slop {
		r.lastClick = time.Time{}
		guigui.DispatchEvent(r, treeRowEventDoubleClick, r.node)
		return guigui.HandleInputResult{}
	}
	r.lastClick = now
	r.clickPos = pos
	return guigui.HandleInputResult{}
}

func (r *treeRow) WriteStateKey(context *guigui.Context, w *guigui.StateKeyWriter) {
	w.WriteString(r.name)
	w.WriteBool(r.visible)
	w.WriteInt(r.node)
}

func (r *treeRow) Build(context *guigui.Context, adder *guigui.ChildAdder) error {
	adder.AddWidget(&r.check)
	adder.AddWidget(&r.text)
	r.check.SetValue(r.visible)
	if r.onCheck == nil {
		r.onCheck = func(context *guigui.Context, value bool) {
			guigui.DispatchEvent(r, treeRowEventVisibility, r.node, value)
		}
	}
	r.check.OnValueChanged(r.onCheck)
	r.text.SetValue(r.name)
	r.text.SetVerticalAlign(basicwidget.VerticalAlignMiddle)
	r.text.SetEllipsisString("…")
	return nil
}

func (r *treeRow) layout(context *guigui.Context) guigui.LinearLayout {
	u := basicwidget.UnitSize(context)
	r.layoutItems = slices.Delete(r.layoutItems, 0, len(r.layoutItems))
	r.layoutItems = append(r.layoutItems,
		guigui.LinearLayoutItem{Widget: &r.check},
		guigui.LinearLayoutItem{Widget: &r.text, Size: guigui.FlexibleSize(1)},
	)
	return guigui.LinearLayout{
		Direction: guigui.LayoutDirectionHorizontal,
		Items:     r.layoutItems,
		Gap:       u / 4,
		Padding:   basicwidget.ListItemTextPadding(context),
	}
}

func (r *treeRow) Layout(context *guigui.Context, widgetBounds *guigui.WidgetBounds, layouter *guigui.ChildLayouter) {
	colorType := basicwidget.ListItemColorTypeDefault
	if v, ok := context.Env(r, basicwidget.EnvKeyListItemColorType); ok {
		if ct, ok := v.(basicwidget.ListItemColorType); ok {
			colorType = ct
		}
	}
	var style basicwidget.TextStyle
	style.SetColor(colorType.TextColor(context))
	r.text.SetBaseStyle(&style)
	// Centre the checkbox vertically.
	b := widgetBounds.Bounds()
	l := r.layout(context)
	l.LayoutWidgets(context, b, layouter)
	cs := r.check.Measure(context, guigui.Constraints{})
	pad := basicwidget.ListItemTextPadding(context)
	y := b.Min.Y + (b.Dy()-cs.Y)/2
	layouter.LayoutWidget(&r.check, image.Rect(b.Min.X+pad.Start, y, b.Min.X+pad.Start+cs.X, y+cs.Y))
}

func (r *treeRow) Measure(context *guigui.Context, constraints guigui.Constraints) image.Point {
	p := r.layout(context).Measure(context, constraints)
	p.Y = max(p.Y, basicwidget.UnitSize(context))
	return p
}
