package meshload

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// addSamples seeds f with the test data files that have the given extension.
func addSamples(f *testing.F, ext string) {
	for _, p := range sampleFiles(ext) {
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
	// The OBJ text, then NULs before the material library and the texture
	// it may refer to.
	addSamples(f, ".obj")
	f.Add([]byte("mtllib m.mtl\nv 0 0 0\nv 1 0 0\nv 1 1 0 0 1 0\nvn 0 0 1\ng a\nusemtl x\nf 1//1 2//1 -1//1\ng b\nf 1/1 2 3 \\\n 1\n\x00newmtl x\nKd 1 0.5\n"))
	f.Add([]byte("mtllib m.mtl\nv 0 0 0\nv 1 0 0\nv 1 1 0\nvt 0 0\nvt 1 0 0\nvt 1 1\nusemtl x\nf 1/1 2/2 3/3\nusemtl y\nf 1/-1 2/-2 3/-3\nf 1 2 3\n\x00newmtl x\nmap_Kd -s 2 2 -clamp on t.png\nnewmtl y\nKd 1 0 0\nmap_Kd -o 0.5 t.png\nmap_d t.png\n\x00" + string(halfPNG(f))))
	f.Fuzz(func(t *testing.T, data []byte) {
		parts := bytes.SplitN(data, []byte{0}, 3)
		parts = append(parts, nil, nil)
		m, err := loadOBJ(parts[0], "fuzz", files(map[string][]byte{"m.mtl": parts[1], "t.png": parts[2]}))
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
	f.Add([]byte(tmfTextured), []byte(tmfPart))
	tex := string(opaquePNG(f))
	f.Fuzz(func(t *testing.T, root, part []byte) {
		data := make3MF(t, map[string]string{
			"_rels/.rels":           tmfRels,
			"3D/main.model":         string(root),
			"3D/Objects/part.model": string(part),
			"3D/Texture/tex.png":    tex,
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

func Fuzz3DS(f *testing.F) {
	addSamples(f, ".3ds")
	f.Add(sample3DS())
	f.Add(texturedTDS())
	// Every texture is the same image.
	tex := halfPNG(f)
	open := func(string) ([]byte, error) { return tex, nil }
	f.Fuzz(func(t *testing.T, data []byte) {
		if m, err := loadTDS(data, "fuzz", open); err == nil {
			checkModel(t, m)
		}
	})
}
