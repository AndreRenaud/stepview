package main

import (
	"testing"

	"github.com/hajimehoshi/ebiten/v2"
)

func TestShadersCompile(t *testing.T) {
	for name, src := range map[string]string{
		"geometry":  geometryShaderSource,
		"ssao":      ssaoShaderSource,
		"blur":      blurShaderSource,
		"composite": compositeShaderSource,
		"fxaa":      fxaaShaderSource,
	} {
		if _, err := ebiten.NewShader([]byte(src)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestMaterialCodes(t *testing.T) {
	for _, tc := range []struct {
		c     [3]float32
		name  string
		metal bool
	}{
		{[3]float32{0.824, 0.820, 0.781}, "C_0402_1005Metric", true}, // KiCad metal grey pins
		{[3]float32{0.859, 0.738, 0.496}, "", true},                  // KiCad gold pins
		{[3]float32{0.735, 0.735, 0.735}, "", true},
		{[3]float32{1, 1, 1}, "", false},
		{[3]float32{0.843, 0.816, 0.753}, "", false}, // connector housing
		{[3]float32{0.754, 0.641, 0.514}, "", false}, // ceramic
		{[3]float32{0.980, 0.627, 0}, "", false},
		{[3]float32{0.148, 0.145, 0.145}, "SOT-23", false},
		{[3]float32{0.313, 0.485, 0.410}, "board_PCB", false},
	} {
		m := guessMaterial(tc.c, tc.name)
		if m == 0 {
			t.Errorf("%v: zero material code", tc.c)
		}
		if got := m&128 != 0; got != tc.metal {
			t.Errorf("%v: metal %v, want %v", tc.c, got, tc.metal)
		}
	}
}
