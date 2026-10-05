//go:build !js

package main

import (
	"errors"

	"github.com/guigui-gui/guigui"
	"github.com/sqweek/dialog"
)

// openDialog asks for a model file with the native file dialog and sends
// it to dialogCh.
func (r *Root) openDialog() {
	if r.dialogCh == nil {
		r.dialogCh = make(chan modelSource, 1)
	}
	go func() {
		// sqweek/dialog runs the native panel on the main thread itself; we
		// only wait for the answer here.
		path, err := dialog.File().
			Title("Open model").
			Filter("STEP files", "step", "stp", "p21").
			Filter("glTF files", "gltf", "glb").
			Filter("Mesh files", "obj", "stl", "3mf", "3ds").
			Load()
		if err != nil {
			if !errors.Is(err, dialog.ErrCancelled) {
				showError("Open failed", err.Error())
			}
			return
		}
		r.dialogCh <- fileSource(path)
	}()
}

// wheelNotch is the wheel movement Ebitengine reports for one notch of a
// mouse wheel.
const wheelNotch = 1

// showError shows an error message and waits for it to be dismissed.
func showError(title, msg string) {
	dialog.Message("%s", msg).Title(title).Error()
}

// setTitle sets the window title.
func setTitle(context *guigui.Context, title string) {
	context.SetWindowTitle(title)
}

// yieldUI lets the user interface run during a long load. Elsewhere than
// the browser it runs on threads of its own, so there is nothing to do.
func yieldUI() {}
