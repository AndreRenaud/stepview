//go:build js

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"strings"
	"sync"
	"syscall/js"
	"testing/fstest"
)

var openDocs = make(chan modelSource, 4)

// watchOpenDocuments returns a channel of the models the page asks to open:
// the files chosen with File > Open… or dropped outside the viewer, and the
// URL in the page's "model" query parameter.
func watchOpenDocuments() <-chan modelSource {
	if page.Truthy() {
		page.Set("openFiles", js.FuncOf(func(this js.Value, args []js.Value) any {
			// Take the File objects now: the list belongs to the page.
			list := args[0]
			files := make([]js.Value, list.Length())
			for i := range files {
				files[i] = list.Index(i)
			}
			go func() {
				fsys, err := readFiles(files)
				if err != nil {
					showError("Open failed", err.Error())
					return
				}
				openDocs <- modelSource{fsys: fsys}
			}()
			return nil
		}))
	}
	if src, ok := urlSource(); ok {
		openDocs <- src
	}
	return openDocs
}

// readFiles reads browser File objects into a flat file system.
func readFiles(files []js.Value) (fs.FS, error) {
	fsys := fstest.MapFS{}
	for _, f := range files {
		buf, err := await(f.Call("arrayBuffer"))
		if err != nil {
			return nil, fmt.Errorf("%s: %v", f.Get("name").String(), err)
		}
		fsys[f.Get("name").String()] = &fstest.MapFile{Data: bytesOf(buf)}
	}
	return fsys, nil
}

// urlSource returns the model named by the "model" query parameter of the
// page, if there is one. The URL is taken relative to the page.
func urlSource() (modelSource, bool) {
	loc := js.Global().Get("location")
	if page.Truthy() {
		loc = page.Get("location")
	}
	base, err := url.Parse(loc.Get("href").String())
	if err != nil {
		return modelSource{}, false
	}
	ref := base.Query().Get("model")
	if ref == "" {
		return modelSource{}, false
	}
	u, err := base.Parse(ref)
	if err != nil {
		return modelSource{}, false
	}
	// The file system is the whole site, so that files the model refers
	// to are found wherever they are on it.
	root := &url.URL{Scheme: u.Scheme, User: u.User, Host: u.Host, Path: "/"}
	return modelSource{fsys: &httpFS{root: root, cache: map[string][]byte{}}, name: strings.TrimPrefix(u.Path, "/")}, true
}

// httpFS reads files from a web site, keeping what it has fetched.
type httpFS struct {
	root  *url.URL
	mu    sync.Mutex
	cache map[string][]byte
}

func (h *httpFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) || name == "." {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	h.mu.Lock()
	data, ok := h.cache[name]
	h.mu.Unlock()
	if !ok {
		var err error
		data, err = fetch(h.root.ResolveReference(&url.URL{Path: name}).String())
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		h.mu.Lock()
		h.cache[name] = data
		h.mu.Unlock()
	}
	return fstest.MapFS{path.Base(name): {Data: data}}.Open(path.Base(name))
}

// ReadDir fails: web servers do not list directories in a standard way.
func (h *httpFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return nil, &fs.PathError{Op: "readdir", Path: name, Err: errors.ErrUnsupported}
}

// fetch downloads a URL.
func fetch(u string) ([]byte, error) {
	resp, err := await(js.Global().Call("fetch", u))
	if err != nil {
		return nil, err
	}
	switch status := resp.Get("status").Int(); {
	case status == 404 || status == 410:
		return nil, fs.ErrNotExist
	case !resp.Get("ok").Bool():
		return nil, fmt.Errorf("HTTP status %d", status)
	}
	buf, err := await(resp.Call("arrayBuffer"))
	if err != nil {
		return nil, err
	}
	return bytesOf(buf), nil
}

// await waits for a JavaScript promise to settle. It must not be called on
// the goroutine handling a JavaScript event.
func await(promise js.Value) (js.Value, error) {
	type result struct {
		v   js.Value
		err error
	}
	ch := make(chan result, 1)
	resolve := js.FuncOf(func(this js.Value, args []js.Value) any {
		ch <- result{v: args[0]}
		return nil
	})
	defer resolve.Release()
	reject := js.FuncOf(func(this js.Value, args []js.Value) any {
		ch <- result{err: errors.New(args[0].Call("toString").String())}
		return nil
	})
	defer reject.Release()
	promise.Call("then", resolve, reject)
	r := <-ch
	return r.v, r.err
}

// bytesOf copies the contents of an ArrayBuffer.
func bytesOf(buf js.Value) []byte {
	a := js.Global().Get("Uint8Array").New(buf)
	b := make([]byte, a.Get("length").Int())
	js.CopyBytesToGo(b, a)
	return b
}
