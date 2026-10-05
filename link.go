package main

import (
	"image"

	"github.com/guigui-gui/guigui"
	"github.com/guigui-gui/guigui/basicwidget"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

var linkEventClicked = guigui.GenerateEventKey()

// link is a line of text that does something when clicked.
type link struct {
	guigui.DefaultWidget

	text  basicwidget.Text
	value string
}

func (l *link) SetValue(s string) {
	l.value = s
}

func (l *link) OnClicked(f func(context *guigui.Context)) {
	guigui.SetEventHandler(l, linkEventClicked, f)
}

func (l *link) WriteStateKey(context *guigui.Context, w *guigui.StateKeyWriter) {
	w.WriteString(l.value)
}

func (l *link) Build(context *guigui.Context, adder *guigui.ChildAdder) error {
	adder.AddWidget(&l.text)
	context.SetPassthrough(&l.text, true)
	var style basicwidget.TextStyle
	style.SetColor(basicwidget.TextColorFromTint(context, basicwidget.AccentTintColor()))
	l.text.SetBaseStyle(&style)
	l.text.SetValue(l.value)
	return nil
}

func (l *link) Measure(context *guigui.Context, constraints guigui.Constraints) image.Point {
	return l.text.Measure(context, constraints)
}

func (l *link) Layout(context *guigui.Context, widgetBounds *guigui.WidgetBounds, layouter *guigui.ChildLayouter) {
	layouter.LayoutWidget(&l.text, widgetBounds.Bounds())
}

func (l *link) HandlePointingInput(context *guigui.Context, widgetBounds *guigui.WidgetBounds) guigui.HandleInputResult {
	if widgetBounds.IsHitAtCursor() && inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		guigui.DispatchEvent(l, linkEventClicked)
		return guigui.HandleInputByWidget(l)
	}
	return guigui.HandleInputResult{}
}

func (l *link) CursorShape(context *guigui.Context, widgetBounds *guigui.WidgetBounds) (ebiten.CursorShapeType, bool) {
	return ebiten.CursorShapePointer, true
}
