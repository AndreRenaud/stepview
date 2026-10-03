//go:build cgo

package main

/*
#include <stdlib.h>

void *stepviewMenuNew(const char *title);
void stepviewMenuAddItem(void *menu, int cmd, const char *title, const char *key, unsigned long mods);
void stepviewMenuAddSeparator(void *menu);
void stepviewMenuAddSubmenu(void *menu, void *sub);
void stepviewMenuAddTop(void *menu);
void stepviewMenuInstall(void);
void stepviewMenuSetState(unsigned int enabled, unsigned int checked);
*/
import "C"

import (
	"unsafe"
)

// Modifier flags for key equivalents (NSEventModifierFlags).
const (
	nsShift   = 1 << 17
	nsControl = 1 << 18
	nsOption  = 1 << 19
	nsCommand = 1 << 20
)

// openHint tells the user how to open a file.
const openHint = "Choose File > Open… to view a model"

var menuCommands = make(chan menuCommand, 16)

// installMenus adds the menus to the menu bar, which then handles their
// shortcuts, and returns the channel of chosen commands. It must be called
// before the UI starts.
func installMenus() (<-chan menuCommand, bool) {
	for _, m := range menus {
		C.stepviewMenuAddTop(buildMenu(m.title, m.items))
	}
	C.stepviewMenuInstall()
	return menuCommands, true
}

func buildMenu(title string, items []menuItem) unsafe.Pointer {
	ct := C.CString(title)
	defer C.free(unsafe.Pointer(ct))
	nm := C.stepviewMenuNew(ct)
	for _, it := range items {
		switch {
		case it.title == separator.title:
			C.stepviewMenuAddSeparator(nm)
		case it.sub != nil:
			C.stepviewMenuAddSubmenu(nm, buildMenu(it.title, it.sub))
		default:
			mods := C.ulong(nsCommand)
			if it.shift {
				mods |= nsShift
			}
			if it.alt {
				mods |= nsOption
			}
			if it.ctrl {
				mods |= nsControl
			}
			key := ""
			if it.key != 0 {
				key = string(it.key)
			} else {
				mods = 0
			}
			ct, ck := C.CString(it.title), C.CString(key)
			C.stepviewMenuAddItem(nm, C.int(it.cmd), ct, ck, mods)
			C.free(unsafe.Pointer(ct))
			C.free(unsafe.Pointer(ck))
		}
	}
	return nm
}

// publishMenuState sets which commands are enabled and checked, as masks
// with bit 1<<cmd for each command.
func publishMenuState(enabled, checked uint32) {
	C.stepviewMenuSetState(C.uint(enabled), C.uint(checked))
}

//export stepviewMenuCommand
func stepviewMenuCommand(cmd C.int) {
	// This runs on the main thread inside the event loop, so never block.
	select {
	case menuCommands <- menuCommand(cmd):
	default:
	}
}
