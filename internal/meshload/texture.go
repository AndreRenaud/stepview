package meshload

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // texture formats
	_ "image/jpeg"
	_ "image/png"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	"github.com/AndreRenaud/stepview/internal/step"
)

// maxTexturePixels bounds the images that are decoded at all, as the
// decoders allocate whatever size a file claims.
const maxTexturePixels = 1 << 26

// DecodeTexture decodes a texture image (PNG, JPEG, GIF, BMP, TIFF, WebP or
// TGA), scaling it down to step.MaxTextureSize. Cutout is left clear: an
// image's alpha only cuts holes when a material says so (see WithMask), as
// it is often just padding around the parts of a texture atlas.
func DecodeTexture(data []byte, name string) (*step.Texture, error) {
	if c, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil && c.Width*c.Height > maxTexturePixels {
		return nil, fmt.Errorf("texture %s: %dx%d image is too large", name, c.Width, c.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		var terr error
		if img, terr = decodeTGA(data); terr != nil {
			return nil, fmt.Errorf("texture %s: %v", name, err)
		}
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, fmt.Errorf("texture %s: empty image", name)
	}
	if w, h := b.Dx(), b.Dy(); w > step.MaxTextureSize || h > step.MaxTextureSize {
		s := float64(step.MaxTextureSize) / float64(max(w, h))
		dst := image.NewNRGBA(image.Rect(0, 0, max(1, int(float64(w)*s)), max(1, int(float64(h)*s))))
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
		img = dst
	}
	return &step.Texture{Name: name, Image: img}, nil
}

// hasHoles reports whether at least 0.1% of an image's pixels are less
// than half opaque.
func hasHoles(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok && o.Opaque() {
		return false
	}
	b := img.Bounds()
	limit := b.Dx() * b.Dy() / 1000
	n := 0
	if p, ok := img.(*image.NRGBA); ok {
		for y := range b.Dy() {
			row := p.Pix[y*p.Stride : y*p.Stride+b.Dx()*4]
			for i := 3; i < len(row); i += 4 {
				if row[i] < 128 {
					if n++; n > limit {
						return true
					}
				}
			}
		}
		return false
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a < 0x8000 {
				if n++; n > limit {
					return true
				}
			}
		}
	}
	return false
}

// WithMask returns a copy of t whose alpha is the opacity of mask
// (stretched to fit), as for OBJ map_d and 3DS opacity maps: the mask's
// alpha channel where it has one (often it is the texture itself), or else
// its brightness. Cutout is set when this leaves holes.
func WithMask(t *step.Texture, mask image.Image) *step.Texture {
	b := t.Image.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), t.Image, b.Min, draw.Src)
	var m []uint8
	if o, ok := mask.(interface{ Opaque() bool }); ok && o.Opaque() {
		g := image.NewGray(out.Bounds())
		draw.ApproxBiLinear.Scale(g, g.Bounds(), mask, mask.Bounds(), draw.Src, nil)
		m = g.Pix
	} else {
		a := image.NewAlpha(out.Bounds())
		draw.ApproxBiLinear.Scale(a, a.Bounds(), mask, mask.Bounds(), draw.Src, nil)
		m = a.Pix
	}
	for i, v := range m {
		out.Pix[i*4+3] = uint8(uint32(out.Pix[i*4+3]) * uint32(v) / 255)
	}
	return &step.Texture{Name: t.Name, Image: out, Cutout: hasHoles(out)}
}

// textures loads and caches the texture files a model refers to.
type textures struct {
	open  func(ref string) ([]byte, error) // see dirOpener
	cache map[string]*step.Texture
	warn  func(format string, args ...any)
}

func newTextures(open func(string) ([]byte, error), warn func(string, ...any)) *textures {
	return &textures{open: open, cache: map[string]*step.Texture{}, warn: warn}
}

// dirOpener reads the files a model in dir refers to (see findFile).
func dirOpener(dir string) func(string) ([]byte, error) {
	return func(ref string) ([]byte, error) {
		path, ok := findFile(dir, ref)
		if !ok {
			return nil, fs.ErrNotExist
		}
		return os.ReadFile(path)
	}
}

// load returns the texture a file refers to, or nil (with a warning) if it
// cannot be found or decoded. mask, if not empty, names an image whose
// brightness becomes the texture's alpha.
func (t *textures) load(ref, mask string) *step.Texture {
	if t == nil || ref == "" {
		return nil
	}
	key := ref + "\x00" + mask
	if tex, ok := t.cache[key]; ok {
		return tex
	}
	t.cache[key] = nil
	data, err := t.open(ref)
	if err != nil {
		t.warn("texture %s: %v", ref, err)
		return nil
	}
	tex, err := DecodeTexture(data, path.Base(strings.ReplaceAll(ref, `\`, "/")))
	if err != nil {
		t.warn("%v", err)
		return nil
	}
	if mask != "" {
		if mt := t.load(mask, ""); mt != nil {
			tex = WithMask(tex, mt.Image)
		}
	}
	t.cache[key] = tex
	return tex
}

// findFile locates a file that a model refers to as ref, which may be an
// absolute or Windows path, or differ in case from the file on disk (as
// old 8.3 names in 3DS files do). It looks for the path relative to dir,
// then for the bare file name in dir and its texture subdirectories.
func findFile(dir, ref string) (string, bool) {
	ref = strings.ReplaceAll(ref, `\`, "/")
	exists := func(p string) bool {
		st, err := os.Stat(p)
		return err == nil && !st.IsDir()
	}
	if !filepath.IsAbs(ref) && !strings.Contains(ref, ":") {
		if p := filepath.Join(dir, filepath.FromSlash(ref)); exists(p) {
			return p, true
		}
	}
	base := ref[strings.LastIndex(ref, "/")+1:]
	if base == "" {
		return "", false
	}
	for _, sub := range []string{"", "textures", "Textures", "maps", "Maps", "images", "tex"} {
		d := filepath.Join(dir, sub)
		if p := filepath.Join(d, base); exists(p) {
			return p, true
		}
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(e.Name(), base) {
				return filepath.Join(d, e.Name()), true
			}
		}
	}
	return "", false
}

// decodeTGA reads an uncompressed or run-length encoded Truevision TGA
// image: true colour (16, 24 or 32 bits) or greyscale.
func decodeTGA(data []byte) (image.Image, error) {
	if len(data) < 18 {
		return nil, errors.New("tga: too short")
	}
	idLen, cmapType, kind := int(data[0]), data[1], data[2]
	w, h := int(binary.LittleEndian.Uint16(data[12:])), int(binary.LittleEndian.Uint16(data[14:]))
	bpp, desc := int(data[16]), data[17]
	rle := kind == 10 || kind == 11
	grey := kind == 3 || kind == 11
	if cmapType != 0 || !(kind == 2 || kind == 3 || kind == 10 || kind == 11) || w == 0 || h == 0 || w*h > maxTexturePixels {
		return nil, errors.New("tga: unsupported image")
	}
	if grey && bpp != 8 || !grey && bpp != 16 && bpp != 24 && bpp != 32 {
		return nil, errors.New("tga: unsupported pixel size")
	}
	bytesPP := bpp / 8
	src := data[18:]
	if idLen > len(src) {
		return nil, errors.New("tga: truncated")
	}
	src = src[idLen:]
	pix := make([]byte, w*h*bytesPP)
	if rle {
		for o := 0; o < len(pix); {
			if len(src) < 1 {
				return nil, errors.New("tga: truncated")
			}
			hdr := src[0]
			src = src[1:]
			n := int(hdr&127) + 1
			if hdr&128 != 0 {
				if len(src) < bytesPP {
					return nil, errors.New("tga: truncated")
				}
				for range n {
					if o+bytesPP > len(pix) {
						break
					}
					copy(pix[o:], src[:bytesPP])
					o += bytesPP
				}
				src = src[bytesPP:]
			} else {
				k := min(n*bytesPP, len(pix)-o)
				if len(src) < k {
					return nil, errors.New("tga: truncated")
				}
				copy(pix[o:], src[:k])
				o += k
				src = src[k:]
			}
		}
	} else {
		if len(src) < len(pix) {
			return nil, errors.New("tga: truncated")
		}
		copy(pix, src)
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	topDown := desc&0x20 != 0
	for y := range h {
		row := y
		if !topDown {
			row = h - 1 - y
		}
		for x := range w {
			p := pix[(row*w+x)*bytesPP:]
			var c color.NRGBA
			switch {
			case grey:
				c = color.NRGBA{p[0], p[0], p[0], 255}
			case bytesPP == 2:
				v := binary.LittleEndian.Uint16(p)
				c = color.NRGBA{uint8(v >> 10 & 31 * 255 / 31), uint8(v >> 5 & 31 * 255 / 31), uint8(v & 31 * 255 / 31), 255}
			case bytesPP == 3:
				c = color.NRGBA{p[2], p[1], p[0], 255}
			default:
				c = color.NRGBA{p[2], p[1], p[0], p[3]}
				if desc&15 == 0 {
					c.A = 255 // no alpha bits
				}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img, nil
}
