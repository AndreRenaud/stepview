// Command stepview is a viewer for STEP (ISO 10303-21) and glTF models.
//
// Usage:
//
//	stepview [file.step]
package main

import (
	"fmt"
	"image"
	"log"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"

	"github.com/guigui-gui/guigui"
	"github.com/hajimehoshi/ebiten/v2"
)

func main() {
	root := &Root{}
	if len(os.Args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: stepview [file.step]")
		os.Exit(2)
	}
	if len(os.Args) == 2 {
		root.pendingPath = argPath(os.Args[1])
	}
	root.openDocs = watchOpenDocuments()
	opts := &guigui.RunOptions{
		Title:         "STEP Viewer",
		WindowSize:    image.Pt(1280, 800),
		WindowMinSize: image.Pt(640, 400),
	}
	if p := os.Getenv("STEPVIEW_CAPTURE"); p != "" {
		root.capture.path = p
		opts.RunGameOptions = &ebiten.RunGameOptions{InitUnfocused: true}
		root.script = os.Getenv("STEPVIEW_SCRIPT")
		if os.Getenv("STEPVIEW_BENCH") != "" {
			root.bench = &benchmark{}
		}
	}
	if p := os.Getenv("STEPVIEW_CPUPROFILE"); p != "" {
		f, err := os.Create(p)
		if err != nil {
			log.Fatal(err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal(err)
		}
		defer pprof.StopCPUProfile()
	}
	if err := guigui.Run(root, opts); err != nil {
		log.Fatal(err)
	}
}

// argPath resolves a path given on the command line. Inside an app bundle
// Ebitengine changes to Contents/Resources before main runs, so a relative
// path is taken from the shell's $PWD instead.
func argPath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	wd, _ := os.Getwd()
	if pwd := os.Getenv("PWD"); filepath.IsAbs(pwd) && strings.HasSuffix(wd, ".app/Contents/Resources") {
		return filepath.Join(pwd, p)
	}
	return p
}
