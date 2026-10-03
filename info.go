package main

import (
	"fmt"
	"image"
	"image/color"
	"strings"

	"github.com/guigui-gui/guigui"
	"github.com/guigui-gui/guigui/basicwidget"
	"github.com/guigui-gui/guigui/basicwidget/basicwidgetdraw"
	"github.com/hajimehoshi/ebiten/v2"

	"github.com/AndreRenaud/stepview/internal/step"
)

// lengthUnit is a unit for showing lengths. Model lengths are in
// millimetres.
type lengthUnit int

const (
	unitMillimetres lengthUnit = iota
	unitCentimetres
	unitMetres
	unitInches
)

var lengthUnits = [...]struct {
	suffix string
	mm     float64 // millimetres per unit
	prec   int     // decimal places shown
}{
	unitMillimetres: {"mm", 1, 2},
	unitCentimetres: {"cm", 10, 3},
	unitMetres:      {"m", 1000, 4},
	unitInches:      {"in", 25.4, 3},
}

// format returns a length given in millimetres as a number in the unit.
func (u lengthUnit) format(mm float64) string {
	d := lengthUnits[u]
	s := fmt.Sprintf("%.*f", d.prec, mm/d.mm)
	if strings.Trim(s, "-0.") == "" {
		// Show a tiny negative value as 0, not -0.
		s = strings.TrimPrefix(s, "-")
	}
	return s
}

// infoText returns the overlay text for a node: the labels and the values
// columns. The position is the centre of the node's bounding box.
func infoText(b step.Box, u lengthUnit) (labels, values string) {
	if b.Empty() {
		return "Size", "No geometry"
	}
	c, s := b.Center(), b.Max.Sub(b.Min)
	suffix := " " + lengthUnits[u].suffix
	return "Position\nSize",
		u.format(c.X) + ", " + u.format(c.Y) + ", " + u.format(c.Z) + suffix + "\n" +
			u.format(s.X) + " × " + u.format(s.Y) + " × " + u.format(s.Z) + suffix
}

// infoPanel shows the name, position and size of the selected node over the
// 3D view. It ignores input, so the view underneath still gets it.
type infoPanel struct {
	guigui.DefaultWidget

	title  basicwidget.Text
	labels basicwidget.Text
	values basicwidget.Text

	titleText, labelsText, valuesText string
}

func (p *infoPanel) set(title, labels, values string) {
	p.titleText, p.labelsText, p.valuesText = title, labels, values
}

func (p *infoPanel) WriteStateKey(context *guigui.Context, w *guigui.StateKeyWriter) {
	w.WriteString(p.titleText)
	w.WriteString(p.labelsText)
	w.WriteString(p.valuesText)
}

func (p *infoPanel) Build(context *guigui.Context, adder *guigui.ChildAdder) error {
	adder.AddWidget(&p.title)
	adder.AddWidget(&p.labels)
	adder.AddWidget(&p.values)
	for _, w := range []guigui.Widget{p, &p.title, &p.labels, &p.values} {
		context.SetPassthrough(w, true)
	}

	var style basicwidget.TextStyle
	style.SetBold(true)
	p.title.SetBaseStyle(&style)
	p.title.SetValue(p.titleText)
	p.title.SetEllipsisString("…")

	style = basicwidget.TextStyle{}
	style.SetColor(basicwidgetdraw.TextColor(context.ColorMode(), false))
	p.labels.SetBaseStyle(&style)
	p.labels.SetMultiline(true)
	p.labels.SetValue(p.labelsText)

	style = basicwidget.TextStyle{}
	style.SetTabular(true)
	p.values.SetBaseStyle(&style)
	p.values.SetMultiline(true)
	p.values.SetValue(p.valuesText)
	return nil
}

// metrics returns the padding, the gap between the columns and the natural
// sizes of the texts.
func (p *infoPanel) metrics(context *guigui.Context) (pad, gap int, title, labels, values image.Point) {
	u := basicwidget.UnitSize(context)
	return u / 3, u / 2,
		p.title.Measure(context, guigui.Constraints{}),
		p.labels.Measure(context, guigui.Constraints{}),
		p.values.Measure(context, guigui.Constraints{})
}

func (p *infoPanel) Measure(context *guigui.Context, constraints guigui.Constraints) image.Point {
	pad, gap, title, labels, values := p.metrics(context)
	// A long name widens the panel only so far; Layout cuts it to fit.
	w := labels.X + gap + values.X
	w = max(w, min(title.X, max(w, basicwidget.UnitSize(context)*12)))
	h := title.Y + max(labels.Y, values.Y)
	return image.Pt(w+2*pad, h+2*pad)
}

func (p *infoPanel) Layout(context *guigui.Context, widgetBounds *guigui.WidgetBounds, layouter *guigui.ChildLayouter) {
	b := widgetBounds.Bounds()
	pad, gap, title, labels, values := p.metrics(context)
	x, y := b.Min.X+pad, b.Min.Y+pad
	// A long name is cut to the panel width.
	layouter.LayoutWidget(&p.title, image.Rect(x, y, b.Max.X-pad, y+title.Y))
	y += title.Y
	layouter.LayoutWidget(&p.labels, image.Rect(x, y, x+labels.X, y+labels.Y))
	x += labels.X + gap
	layouter.LayoutWidget(&p.values, image.Rect(x, y, x+values.X, y+values.Y))
}

func (p *infoPanel) Draw(context *guigui.Context, widgetBounds *guigui.WidgetBounds, dst *ebiten.Image) {
	clr := color.NRGBAModel.Convert(basicwidgetdraw.PopupBackgroundColor(context.ColorMode())).(color.NRGBA)
	clr.A = 220
	basicwidgetdraw.DrawRoundedRect(context, dst, widgetBounds.Bounds(), clr, basicwidget.RoundedCornerRadius(context))
}
