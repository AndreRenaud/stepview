//go:build js

package main

import "syscall/js"

// openHint tells the user how to open a file.
const openHint = "Drop a model here, or choose File > Open…"

var menuCommands = make(chan menuCommand, 16)

// page is the API of the web page around the viewer (web/index.html), or
// undefined when the viewer runs on its own.
var page = js.Global().Get("stepview")

// installMenus gives the menus to the page, which shows them, handles their
// shortcuts and returns the chosen commands on the channel. Without a page
// (false) the shortcuts are read from the keyboard instead.
func installMenus() (<-chan menuCommand, bool) {
	if !page.Truthy() {
		return nil, false
	}
	var ms []any
	for _, m := range menus {
		ms = append(ms, map[string]any{"title": m.title, "items": menuItemsJS(m.items)})
	}
	page.Call("installMenus", ms, int(cmdOpen), js.FuncOf(func(this js.Value, args []js.Value) any {
		// This runs in a JavaScript event handler, so never block.
		select {
		case menuCommands <- menuCommand(args[0].Int()):
		default:
		}
		return nil
	}))
	return menuCommands, true
}

// menuItemsJS converts menu items for the page, leaving out Exit: a web
// page cannot close itself.
func menuItemsJS(items []menuItem) []any {
	var out []any
	for _, it := range items {
		switch {
		case it.title == separator.title:
			if len(out) > 0 {
				out = append(out, map[string]any{"separator": true})
			}
		case it.sub != nil:
			out = append(out, map[string]any{"title": it.title, "items": menuItemsJS(it.sub)})
		case it.cmd != cmdExit:
			key := ""
			if it.key != 0 {
				key = string(it.key)
			}
			out = append(out, map[string]any{
				"title": it.title, "cmd": int(it.cmd), "key": key,
				"shift": it.shift, "alt": it.alt, "ctrl": it.ctrl,
			})
		}
	}
	// Drop a trailing separator, left where Exit was.
	if n := len(out); n > 0 && out[n-1].(map[string]any)["separator"] == true {
		out = out[:n-1]
	}
	return out
}

// publishMenuState sets which commands are enabled and checked, as masks
// with bit 1<<cmd for each command.
func publishMenuState(enabled, checked uint32) {
	if page.Truthy() {
		page.Call("setMenuState", enabled, checked)
	}
}
