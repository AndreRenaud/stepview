//go:build js

package main

import (
	"sync/atomic"
	"syscall/js"
	"time"

	"github.com/guigui-gui/guigui"
)

// openDialog asks the page to show its file dialog. The page normally does
// that itself when File > Open… is chosen, as browsers only show one in
// response to a click or key press.
func (r *Root) openDialog() {
	if page.Truthy() {
		page.Call("openFileDialog")
	}
}

// wheelNotch is the wheel movement Ebitengine reports for one notch of a
// mouse wheel: in a browser it is the page's scroll distance in pixels.
const wheelNotch = 100

// showError shows an error message and waits for it to be dismissed.
func showError(title, msg string) {
	js.Global().Call("alert", title+"\n\n"+msg)
}

// setTitle sets the page's title.
func setTitle(context *guigui.Context, title string) {
	if page.Truthy() {
		page.Call("setTitle", title)
		return
	}
	js.Global().Get("document").Set("title", title)
}

// lastYield is when yieldUI last let the browser run, in Unix nanoseconds.
var lastYield atomic.Int64

// yieldUI lets the browser draw a frame now and then during a long load.
// WebAssembly runs Go on the browser's only thread and cannot preempt a
// goroutine, so nothing else happens until the load blocks.
func yieldUI() {
	const every = 100 * time.Millisecond
	if time.Now().UnixNano()-lastYield.Load() < int64(every) {
		return
	}
	// Wait for the next frame, when Ebitengine draws. A hidden page has no
	// frames, so a timer ends the wait too.
	ch := make(chan struct{}, 1)
	f := js.FuncOf(func(this js.Value, args []js.Value) any {
		select {
		case ch <- struct{}{}:
		default:
		}
		return nil
	})
	global := js.Global()
	frame := global.Call("requestAnimationFrame", f)
	timer := global.Call("setTimeout", f, 100)
	<-ch
	global.Call("cancelAnimationFrame", frame)
	global.Call("clearTimeout", timer)
	f.Release()
	lastYield.Store(time.Now().UnixNano())
}
