// Command stepinfo loads a STEP file, prints statistics and the product
// tree, and can render a preview image. It is a debugging aid for the
// step package.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/AndreRenaud/stepview/internal/step"
)

func main() {
	pngPath := flag.String("png", "", "render a preview to this PNG file")
	size := flag.Int("size", 800, "preview size in pixels")
	yaw := flag.Float64("yaw", 35, "view yaw in degrees")
	pitch := flag.Float64("pitch", 30, "view pitch in degrees")
	depth := flag.Int("depth", 3, "tree depth to print")
	warnings := flag.Int("warnings", 10, "number of warnings to print")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: stepinfo [flags] file.step")
		os.Exit(2)
	}
	m, err := step.LoadFile(flag.Arg(0), step.DefaultOptions())
	if err != nil {
		log.Fatal(err)
	}
	s := m.Stats
	fmt.Printf("name=%q entities=%d solids=%d faces=%d failed=%d triangles=%d parse=%v tess=%v\n",
		m.Name, s.Entities, s.Solids, s.Faces, s.FailedFaces, s.Triangles, s.ParseTime, s.TessTime)
	for _, r := range m.Roots {
		printTree(r, step.Identity(), 0, *depth)
	}
	if len(m.Warnings) > 0 {
		counts := map[string]int{}
		for _, w := range m.Warnings {
			k := w
			if i := strings.Index(w, ": "); i >= 0 {
				k = w[i+2:]
			}
			counts[k]++
		}
		type kv struct {
			k string
			n int
		}
		var kvs []kv
		for k, n := range counts {
			kvs = append(kvs, kv{k, n})
		}
		sort.Slice(kvs, func(i, j int) bool { return kvs[i].n > kvs[j].n })
		fmt.Printf("warnings (%d):\n", len(m.Warnings))
		for i, e := range kvs {
			if i >= *warnings {
				break
			}
			fmt.Printf("  %4d x %s\n", e.n, e.k)
		}
		for i, w := range m.Warnings {
			if i >= *warnings {
				break
			}
			fmt.Printf("  %s\n", w)
		}
	}
	if *pngPath != "" {
		img := render(m, *size, *yaw*math.Pi/180, *pitch*math.Pi/180)
		f, err := os.Create(*pngPath)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, img); err != nil {
			log.Fatal(err)
		}
	}
}

func worldBox(n *step.Node, xf step.Affine) step.Box {
	w := xf.Mul(n.Local)
	b := step.EmptyBox()
	if n.Mesh != nil {
		b.Union(n.Mesh.Bounds.Transform(w))
	}
	for _, c := range n.Children {
		b.Union(worldBox(c, w))
	}
	return b
}

func printTree(n *step.Node, xf step.Affine, d, max int) {
	if d > max {
		return
	}
	tris := 0
	if n.Mesh != nil {
		tris = n.Mesh.TriangleCount()
	}
	b := worldBox(n, xf)
	fmt.Printf("%s%s (%d tris, %d children) box=[%.1f %.1f %.1f]-[%.1f %.1f %.1f]\n", strings.Repeat("  ", d), n.Name, tris, len(n.Children),
		b.Min.X, b.Min.Y, b.Min.Z, b.Max.X, b.Max.Y, b.Max.Z)
	for i, c := range n.Children {
		if i >= 20 {
			fmt.Printf("%s  ... %d more\n", strings.Repeat("  ", d), len(n.Children)-i)
			break
		}
		printTree(c, xf.Mul(n.Local), d+1, max)
	}
}

type tri struct {
	p   [3]step.Vec3
	n   [3]step.Vec3
	col [3]float32
}

func collect(n *step.Node, xf step.Affine, out *[]tri) {
	w := xf.Mul(n.Local)
	if m := n.Mesh; m != nil {
		for i := 0; i+2 < len(m.Indices); i += 3 {
			var t tri
			for k := 0; k < 3; k++ {
				v := m.Indices[i+k]
				p := step.Vec3{X: float64(m.Positions[v*3]), Y: float64(m.Positions[v*3+1]), Z: float64(m.Positions[v*3+2])}
				nn := step.Vec3{X: float64(m.Normals[v*3]), Y: float64(m.Normals[v*3+1]), Z: float64(m.Normals[v*3+2])}
				t.p[k] = w.Apply(p)
				t.n[k] = w.ApplyNormal(nn)
				if k == 0 {
					t.col = [3]float32{m.Colors[v*3], m.Colors[v*3+1], m.Colors[v*3+2]}
				}
			}
			*out = append(*out, t)
		}
	}
	for _, c := range n.Children {
		collect(c, w, out)
	}
}

// render draws the model with a simple z-buffered rasterizer, using an
// orthographic view with Z up.
func render(m *step.Model, size int, yaw, pitch float64) *image.RGBA {
	var tris []tri
	for _, r := range m.Roots {
		collect(r, step.Identity(), &tris)
	}
	// View basis.
	fwd := step.Vec3{X: -math.Cos(pitch) * math.Cos(yaw), Y: -math.Cos(pitch) * math.Sin(yaw), Z: -math.Sin(pitch)}
	right := fwd.Cross(step.Vec3{Z: 1}).Norm()
	up := right.Cross(fwd)
	box := step.EmptyBox()
	for _, t := range tris {
		for _, p := range t.p {
			box.Extend(step.Vec3{X: p.Dot(right), Y: p.Dot(up), Z: p.Dot(fwd)})
		}
	}
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	if box.Empty() {
		return img
	}
	span := math.Max(box.Max.X-box.Min.X, box.Max.Y-box.Min.Y) * 1.05
	cx, cy := (box.Min.X+box.Max.X)/2, (box.Min.Y+box.Max.Y)/2
	scale := float64(size) / span
	zbuf := make([]float64, size*size)
	for i := range zbuf {
		zbuf[i] = math.Inf(1)
	}
	light := fwd.Scale(-1).Add(up.Scale(0.5)).Add(right.Scale(-0.3)).Norm()
	for _, t := range tris {
		var sx, sy, sz [3]float64
		for k := 0; k < 3; k++ {
			p := t.p[k]
			sx[k] = (p.Dot(right)-cx)*scale + float64(size)/2
			sy[k] = float64(size)/2 - (p.Dot(up)-cy)*scale
			sz[k] = p.Dot(fwd)
		}
		minX := int(math.Max(0, math.Floor(math.Min(sx[0], math.Min(sx[1], sx[2])))))
		maxX := int(math.Min(float64(size-1), math.Ceil(math.Max(sx[0], math.Max(sx[1], sx[2])))))
		minY := int(math.Max(0, math.Floor(math.Min(sy[0], math.Min(sy[1], sy[2])))))
		maxY := int(math.Min(float64(size-1), math.Ceil(math.Max(sy[0], math.Max(sy[1], sy[2])))))
		area := (sx[1]-sx[0])*(sy[2]-sy[0]) - (sx[2]-sx[0])*(sy[1]-sy[0])
		if math.Abs(area) < 1e-12 {
			continue
		}
		for y := minY; y <= maxY; y++ {
			for x := minX; x <= maxX; x++ {
				px, py := float64(x)+0.5, float64(y)+0.5
				w0 := ((sx[1]-px)*(sy[2]-py) - (sx[2]-px)*(sy[1]-py)) / area
				w1 := ((sx[2]-px)*(sy[0]-py) - (sx[0]-px)*(sy[2]-py)) / area
				w2 := 1 - w0 - w1
				if w0 < 0 || w1 < 0 || w2 < 0 {
					continue
				}
				z := w0*sz[0] + w1*sz[1] + w2*sz[2]
				if z >= zbuf[y*size+x] {
					continue
				}
				zbuf[y*size+x] = z
				n := t.n[0].Scale(w0).Add(t.n[1].Scale(w1)).Add(t.n[2].Scale(w2)).Norm()
				// Back faces are drawn in red to expose orientation errors.
				facing := n.Dot(fwd) < 0
				d := math.Abs(n.Dot(light))
				sh := 0.25 + 0.75*d
				r, g, b := float64(t.col[0])*sh, float64(t.col[1])*sh, float64(t.col[2])*sh
				if !facing {
					r, g, b = 0.9*sh, 0.1, 0.1
				}
				img.Set(x, y, color.RGBA{uint8(math.Min(255, r*255)), uint8(math.Min(255, g*255)), uint8(math.Min(255, b*255)), 255})
			}
		}
	}
	return img
}
