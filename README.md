# stepview

A STEP (ISO 10303-21) model viewer written in Go, using
[guigui](https://github.com/guigui-gui/guigui) and
[Ebitengine](https://ebitengine.org) for the UI and rendering, and
[sqweek/dialog](https://github.com/sqweek/dialog) for the native file dialog.
glTF (`.gltf`/`.glb`), Wavefront OBJ (`.obj` with `.mtl` materials), STL
(binary and ASCII), 3MF and 3D Studio (`.3ds`) files can be opened too.

```sh
go run . [model.step]
```

## Building

```sh
make            # bin/stepview and bin/stepinfo
make test
make app        # build/StepView.app (make universal for arm64 + amd64)
make install    # copy the app into /Applications
make web        # build/web, the browser version (see below)
```

### Web version

`make web` builds the viewer for WebAssembly into `build/web`, a static site
that can be published as is (GitHub Pages, S3, any web server):

| File | Purpose |
| --- | --- |
| `index.html` | the page: menu bar, file dialog and shortcuts around the viewer |
| `viewer.html` | runs the viewer in an iframe, full frame, as Ebitengine expects |
| `stepview.wasm` | the viewer (about 26 MB, 6.5 MB compressed) |
| `wasm_exec.js` | Go's WebAssembly support, from the Go that built it |

`make serve` builds it and serves it on <http://localhost:8080>. The server
should send `.wasm` files as `application/wasm` and compressed; the page
still works without either, but starts more slowly.

Drop files on the page, or choose File > Open…, to view them. Files are read
in the browser and never uploaded. Drop a model together with the files it
refers to (an OBJ's `.mtl` and textures, a glTF's `.bin` and images), or
drop the folder holding them. A model on the web can be opened with the
`model` parameter, relative to the page, e.g.
`index.html?model=samples/part.step`; the files it refers to are fetched
from the same site.

## Using it

- **File > Open…** (⌘O) chooses a file; a path on the command line is loaded
  at startup. Files can also be dropped on the window. With nothing open,
  **Load Benchy Demo** shows the built-in demo model.
  On macOS the app bundle also opens any of these files double-clicked in
  Finder or dropped on its Dock icon.
- The tree on the left shows the product structure. Click an item to select
  (highlight) it; use its checkbox to hide or show it and everything below it.
  Double-click an item to centre it in the view without changing the zoom or
  orientation. Drag the gap between the tree and the view to resize it.
- In the 3D view:
  - left-drag rotates, right-drag (or middle-drag) pans, the wheel zooms
    towards the cursor;
  - left-click selects the part under the cursor and reveals it in the tree;
    clicking empty space clears the selection;
  - `F` fits the whole model, `S` fits the selection and `W` toggles
    wireframe (after clicking the view).
- The **View** menu:

  | Item | Shortcut |
  | --- | --- |
  | Show Sidebar (the tree and status line) | ⌃⌘S |
  | Fit All / Fit Selection | ⌘F / ⇧⌘F |
  | Iso / Top / Front / Right | ⌘1 – ⌘4 |
  | Spin | ⌘R |
  | Quality > Normal / Wireframe / High Quality | ⌥⌘1 – ⌥⌘3 |
  | Show All Parts (un-hide everything) | ⇧⌘H |

  Wireframe draws the model edges (B-rep face boundaries, or sharp creases
  for mesh formats), with the selection drawn in orange. Hide the sidebar and
  use Window > Enter Full Screen to see only the model.
- Elsewhere than macOS there is no menu bar, but the same shortcuts work with
  Ctrl in place of ⌘ and Alt in place of ⌥ or ⌃.

## How it works

`internal/step` is a pure-Go STEP reader with no C dependencies:

- `parse.go`: a parallel ISO 10303-21 parser (complex entities, typed
  parameters and string encodings).
- `model.go`: the product structure. It handles `NEXT_ASSEMBLY_USAGE_OCCURRENCE`
  with context-dependent placements as well as mapped-item assemblies, unit
  conversion (mm, inch, degrees) and colours from styled items.
- `geometry.go`, `bspline.go`: curves (line, circle, ellipse, B-spline,
  polyline, composite) and surfaces (plane, cylinder, cone, sphere, torus,
  rational B-spline, extrusion, revolution, offset).
- `tessellate.go`, `cdt.go`: each face's boundary is mapped into the surface's
  parameter space, seams and poles are resolved, and the result is
  triangulated with a constrained Delaunay triangulation. Curved faces are then
  refined until the chordal error is within tolerance. Edges are sampled once
  and shared, so adjacent faces meet without cracks.

`internal/gltfload` and `internal/meshload` turn glTF, OBJ, STL, 3MF and 3DS
files into the same model tree. They read through an `fs.FS`, which holds
the files a model refers to as well: the disk, the files dropped on the
window, or a web site. OBJ groups, 3MF build items and components,
and the 3DS keyframer hierarchy become tree nodes. Colours come from MTL and
3DS material diffuse colours, STL facet colours (VisCAM and Materialise
conventions) and 3MF base materials and colour groups. Vertices are welded
and, where the file has no normals, smoothed using 3DS smoothing groups or
else between triangles that meet at less than 35°. A closed mesh that is
inside out is turned the right way round. OBJ is turned from Y up to Z up;
OBJ, STL and 3DS have no units and are taken as millimetres.

Base colour textures are read from glTF, OBJ (`map_Kd`), 3DS and 3MF
(`texture2dgroup`) files, in PNG, JPEG, GIF, BMP, TIFF, WebP or TGA, and
scaled down to at most 4096 pixels. Texture references that use Windows
paths, the wrong case or a `Maps`/`textures` subdirectory are still found.
A texture's alpha only cuts holes when the material asks for it (glTF
`MASK`, OBJ `map_d`, 3DS opacity maps); otherwise it is ignored.

Transparency comes from STEP's `SURFACE_STYLE_TRANSPARENT`, glTF's `BLEND`
mode, OBJ's `d`/`Tr`, 3DS material transparency and 3MF colours with
alpha.

The viewer (package `main`) flattens the tree and places every part's
vertices in world space, with a fixed studio lighting rig baked into their
colours. Each frame (`render.go`) projects them on all cores, drops triangles
that face away or are off screen, cuts those crossing the near plane, and
draws the rest, one draw call per texture. Texture coordinates travel
divided by depth, so the shader can recover them with perspective, and
are filtered bilinearly in the shader. Transparent triangles are drawn
afterwards: sorted from far to near on the CPU, both sides of them, and
blended over the opaque picture where they are in front of it. Ebitengine has no depth buffer, so the nearest surface is
found in three passes that build a 24-bit depth one byte at a time with a
"max" blend, followed by a colour pass. Picking is a CPU ray cast against the
original triangles.

`cmd/stl2step` joins mesh files into a STEP assembly, one faceted B-rep part
per file, optionally simplified (by quadric edge collapse) and coloured. The
demo model, `demo/3DBenchy.step.gz`, is the multi-part
[#3DBenchy](https://github.com/CreativeTools/3DBenchy) by Creative Tools,
which is in the public domain (CC0), made with it by `demo/benchy.sh`:

```sh
git clone https://github.com/CreativeTools/3DBenchy
demo/benchy.sh 3DBenchy
```

`cmd/stepinfo` is a debugging tool. It prints load statistics and the product
tree, and can render a preview PNG without opening a window:

```sh
go run ./cmd/stepinfo -png preview.png model.step
```

## Development aids

These environment variables are for testing the GUI non-interactively:

| Variable | Effect |
| --- | --- |
| `STEPVIEW_CAPTURE=out.png` | open unfocused, save a screenshot after loading, then exit |
| `STEPVIEW_VIEW=yaw,pitch` | initial camera angles in degrees |
| `STEPVIEW_SCRIPT=...` | with `CAPTURE`: comma separated `pick`, `select:N`, `center:N`, `hide:N`, `zoom:F`, `wire` |
| `STEPVIEW_BENCH=1` | with `CAPTURE`: spin the camera and log the frame rate |
| `STEPVIEW_CPUPROFILE=cpu.prof` | write a CPU profile |

`testdata/` has sample files from the
[STEP Tools sample collection](https://www.steptools.com/docs/stpfiles/ap203/index.html),
with their reference images, and OBJ, STL, 3MF and 3DS samples listed with
their licenses in [testdata/SOURCES.md](testdata/SOURCES.md). `go test ./...`
loads all of them.

## Limitations

- Vertices are projected on the CPU, and Ebitengine copies them for each of
  the four passes. A PCB with ~250k triangles takes about 8 ms per frame on
  an M1 Pro. Rendering only happens when the view changes.
- Faces whose orientation is reversed in the file are culled as back faces.
- OBJ polygons are fan triangulated, so concave ones may be filled wrongly.
  3MF composite materials and extensions other than materials and
  production are ignored, as are 3DS keyframer transforms (an object
  instanced more than once is shown once).
- Only base colour textures are used, without mipmaps, so a texture seen
  from far away can shimmer. Cutouts use a fixed alpha of one half.
  Textures always repeat (no clamping or decals).
- Transparent triangles are sorted by their centres, so intersecting or
  interleaved transparent surfaces can blend in the wrong order. In high
  quality mode they get the simple lighting of normal mode, without
  ambient occlusion.
- In the browser, Go runs on the page's only thread, so loading is several
  times slower than on the desktop, and the page stops responding while a
  STEP file is parsed.
- Only B-rep and faceted geometry is shown. Wireframe, PMI and tessellated
  (AP242 `TESSELLATED_*`) data are ignored.
