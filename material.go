package main

import (
	"math"
	"strings"

	"github.com/AndreRenaud/stepview/internal/step"
)

// A material code describes a surface for the high quality renderer in one
// byte: bit 7 is set for metals and bits 0-6 hold the roughness. It is
// never zero, so a zero in the G-buffer marks the background.
type material uint8

func newMaterial(metal bool, roughness float32) material {
	r := material(math.Round(float64(min(1, max(0.05, roughness)) * 127)))
	if metal {
		r |= 128
	}
	return r
}

var (
	matPlastic     = newMaterial(false, 0.5)
	matDarkPlastic = newMaterial(false, 0.42)
	matSolderMask  = newMaterial(false, 0.28)
	matTin         = newMaterial(true, 0.35)
	matGold        = newMaterial(true, 0.28)
)

// Colours of the standard KiCad 3D model library, which most ECAD exports
// reuse, with the materials they stand for.
var kicadMaterials = []struct {
	c [3]float32
	m material
}{
	{[3]float32{0.824, 0.820, 0.781}, matTin},  // metal grey pins
	{[3]float32{0.859, 0.738, 0.496}, matGold}, // gold pins
	{[3]float32{0.313, 0.485, 0.410}, matSolderMask},
}

// guessMaterial picks a plausible material for a surface of colour c (in
// sRGB) on a part called name, as STEP files only record colours.
func guessMaterial(c [3]float32, name string) material {
	for _, k := range kicadMaterials {
		if abs32(c[0]-k.c[0]) < 0.004 && abs32(c[1]-k.c[1]) < 0.004 && abs32(c[2]-k.c[2]) < 0.004 {
			return k.m
		}
	}
	hi := max(c[0], c[1], c[2])
	lo := min(c[0], c[1], c[2])
	sat := float32(0)
	if hi > 0 {
		sat = (hi - lo) / hi
	}
	switch {
	case sat < 0.03 && hi >= 0.6 && hi <= 0.93:
		// Neutral light greys are tin plated pins, shields and the like;
		// white is plastic.
		return matTin
	case sat >= 0.35 && sat <= 0.86 && hi >= 0.6 && c[0] >= c[1] && c[1] >= c[2] && hue(c) >= 15 && hue(c) <= 55:
		// Gold, brass and copper.
		return matGold
	}
	lname := strings.ToLower(name)
	if strings.Contains(lname, "pcb") || strings.Contains(lname, "board") {
		return matSolderMask
	}
	if hi < 0.3 {
		return matDarkPlastic
	}
	return matPlastic
}

// meshMaterial returns the material of a mesh's surface of colour c,
// using the mesh's own description when it has one.
func meshMaterial(m *step.Mesh, c [3]float32, name string) material {
	if m.HasPBR {
		return newMaterial(m.Metallic >= 0.5, m.Roughness)
	}
	return guessMaterial(c, name)
}

// hue returns the hue of an RGB colour in degrees.
func hue(c [3]float32) float32 {
	hi := max(c[0], c[1], c[2])
	d := hi - min(c[0], c[1], c[2])
	if d == 0 {
		return 0
	}
	var h float32
	switch hi {
	case c[0]:
		h = (c[1] - c[2]) / d
	case c[1]:
		h = 2 + (c[2]-c[0])/d
	default:
		h = 4 + (c[0]-c[1])/d
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return h
}

func abs32(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}
