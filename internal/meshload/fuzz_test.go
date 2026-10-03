package meshload

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// addSamples seeds f with the test data files that have the given extension.
func addSamples(f *testing.F, ext string) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "*"+ext))
	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
}

func FuzzSTL(f *testing.F) {
	addSamples(f, ".stl")
	tri := [][3][3]float32{{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}}}
	f.Add(binarySTL("COLOR=\x10\x20\x30\xff", tri, 0x1234))
	f.Add(binarySTL("solid x", tri, 0x8000|0x1234))
	f.Add([]byte("solid a\nfacet normal 0 0 1\nouter loop\nvertex 0 0 0\nvertex 1 0 0\nvertex 1 1 0\nvertex 0 1 0\nendloop\nendfacet\nendsolid\nsolid b\nvertex nan 0 0\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if m, err := loadSTL(data, "fuzz"); err == nil {
			checkModel(t, m)
		}
	})
}

func FuzzOBJ(f *testing.F) {
	// The OBJ text, then a NUL and the material library it may refer to.
	addSamples(f, ".obj")
	f.Add([]byte("mtllib m.mtl\nv 0 0 0\nv 1 0 0\nv 1 1 0 0 1 0\nvn 0 0 1\ng a\nusemtl x\nf 1//1 2//1 -1//1\ng b\nf 1/1 2 3 \\\n 1\n\x00newmtl x\nKd 1 0.5\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		obj, mtl, _ := bytes.Cut(data, []byte{0})
		m, err := loadOBJ(obj, "fuzz", func(name string) ([]byte, error) {
			if name != "m.mtl" {
				return nil, fs.ErrNotExist
			}
			return mtl, nil
		})
		if err == nil {
			checkModel(t, m)
		}
	})
}

func Fuzz3MF(f *testing.F) {
	// The root model part and a second part that components may refer to;
	// the zip container itself is not fuzzed.
	files, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "*.3mf"))
	for _, p := range files {
		zr, err := zip.OpenReader(p)
		if err != nil {
			f.Fatal(err)
		}
		for _, zf := range zr.File {
			if filepath.Ext(zf.Name) == ".model" {
				rc, _ := zf.Open()
				var buf bytes.Buffer
				buf.ReadFrom(rc)
				rc.Close()
				f.Add(buf.Bytes(), []byte(tmfPart))
			}
		}
		zr.Close()
	}
	f.Add([]byte(tmfRoot), []byte(tmfPart))
	f.Fuzz(func(t *testing.T, root, part []byte) {
		data := make3MF(t, map[string]string{
			"_rels/.rels":           tmfRels,
			"3D/main.model":         string(root),
			"3D/Objects/part.model": string(part),
		})
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		if m, err := load3MF(zr, "fuzz"); err == nil {
			checkModel(t, m)
		}
	})
}
