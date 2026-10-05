package main

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"io"
	"io/fs"
	"testing/fstest"
)

// benchy is the demo model: #3DBenchy by Creative Tools, which is in the
// public domain, as a STEP assembly of its parts made by demo/benchy.sh.
//
//go:embed demo/3DBenchy.step.gz
var benchy []byte

// demoSource is the demo model.
func demoSource() modelSource {
	return modelSource{fsys: gzipFS{name: "3DBenchy.step", data: benchy}, name: "3DBenchy.step"}
}

// gzipFS holds one file, compressed, and decompresses it when it is opened
// (on the loading goroutine).
type gzipFS struct {
	name string
	data []byte
}

func (g gzipFS) Open(name string) (fs.File, error) {
	if name != g.name {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	zr, err := gzip.NewReader(bytes.NewReader(g.data))
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	return fstest.MapFS{name: {Data: data}}.Open(name)
}
