package main

import (
	"errors"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/AndreRenaud/stepview/internal/gltfload"
	"github.com/AndreRenaud/stepview/internal/meshload"
	"github.com/AndreRenaud/stepview/internal/step"
)

// modelSource is a model to load: the file name in fsys, which also holds
// the files the model refers to. An empty name means the model is to be
// found among the files in fsys, as when several files are dropped at once.
type modelSource struct {
	fsys fs.FS
	name string
}

// fileSource is the model in a file on disk.
func fileSource(path string) modelSource {
	fsys, name := meshload.OSFS(path)
	return modelSource{fsys: fsys, name: name}
}

// modelExts are the extensions of the files that can be opened, in the
// order findModel prefers them.
var modelExts = []string{".step", ".stp", ".p21", ".gltf", ".glb", ".3mf", ".obj", ".3ds", ".stl"}

// findModel returns the name of the model among the files in fsys: the one
// whose extension comes first in modelExts, nearest the top.
func findModel(fsys fs.FS) (string, error) {
	best, bestRank := "", len(modelExts)
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rank := slices.Index(modelExts, strings.ToLower(path.Ext(p)))
		if rank < 0 {
			return nil
		}
		if rank < bestRank || rank == bestRank && strings.Count(p, "/") < strings.Count(best, "/") {
			best, bestRank = p, rank
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if best == "" {
		return "", errors.New("no STEP, glTF, OBJ, STL, 3MF or 3DS file found")
	}
	return best, nil
}

// loadModel reads a STEP, glTF, OBJ, STL, 3MF or 3DS file.
func loadModel(fsys fs.FS, name string, progress func(string, float64)) (*step.Model, error) {
	switch strings.ToLower(path.Ext(name)) {
	case ".gltf", ".glb":
		return gltfload.LoadFS(fsys, name)
	case ".obj", ".stl", ".3mf", ".3ds":
		return meshload.LoadFS(fsys, name)
	}
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, err
	}
	opt := step.DefaultOptions()
	opt.Progress = progress
	return step.Load(data, opt)
}
