package main

import (
	"testing"
	"testing/fstest"
)

func TestFindModel(t *testing.T) {
	for _, tc := range []struct {
		files []string
		want  string
	}{
		{[]string{"a.obj", "a.mtl", "tex.png"}, "a.obj"},
		{[]string{"part.STL", "assembly.step"}, "assembly.step"},
		{[]string{"model/textures/x.png", "model/x.gltf", "model/x.bin"}, "model/x.gltf"},
		{[]string{"deep/er/b.stl", "c.stl"}, "c.stl"},
		{[]string{"readme.txt", "photo.jpg"}, ""},
	} {
		fsys := fstest.MapFS{}
		for _, f := range tc.files {
			fsys[f] = &fstest.MapFile{}
		}
		got, err := findModel(fsys)
		if tc.want == "" {
			if err == nil {
				t.Errorf("%v: found %s", tc.files, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%v: got %q, %v; want %q", tc.files, got, err, tc.want)
		}
	}
}

func TestDemo(t *testing.T) {
	src := demoSource()
	m, err := loadModel(src.fsys, src.name, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Roots) != 1 || len(m.Roots[0].Children) != 17 {
		t.Fatalf("got %d roots, want one with 17 parts", len(m.Roots))
	}
	if m.Stats.FailedFaces > 0 {
		t.Errorf("%d faces failed", m.Stats.FailedFaces)
	}
}
