//go:build !darwin || !cgo

package main

import "runtime"

// openHint tells the user how to open a file.
var openHint = map[bool]string{
	true:  "Press Cmd+O to open a model",
	false: "Press Ctrl+O to open a model",
}[runtime.GOOS == "darwin"]

// installMenus returns false: there is no native menu bar, so the
// shortcuts are read from the keyboard instead.
func installMenus() (<-chan menuCommand, bool) {
	return nil, false
}

func publishMenuState(enabled, checked uint32) {}
