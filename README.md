# stepview

A STEP (ISO 10303-21) model viewer written in Go, using
[guigui](https://github.com/guigui-gui/guigui) and
[Ebitengine](https://ebitengine.org) for the UI and rendering, and
[sqweek/dialog](https://github.com/sqweek/dialog) for the native file dialog.
glTF (`.gltf`/`.glb`), Wavefront OBJ (`.obj` with `.mtl` materials), STL
(binary and ASCII) and 3MF files can be opened too.

```sh
go run . [model.step]
```

## Building

```sh
make            # bin/stepview and bin/stepinfo
make test
make app        # build/StepView.app (make universal for arm64 + amd64)
make install    # copy the app into /Applications
```

## Using it

- **Open…** chooses a file; a path on the command line is loaded at startup.
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
- **Fit / Iso / Top / Front / Right** set the camera; **Show all** un-hides
  everything; **Wireframe** switches between shaded rendering and a
  wireframe of the model edges (B-rep face boundaries, or sharp creases for
  mesh formats), with the selection drawn in orange.

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

`internal/gltfload` and `internal/meshload` turn glTF, OBJ, STL and 3MF
files into the same model tree. OBJ groups and 3MF build items and
components become tree nodes, and colours come from MTL diffuse colours, STL
facet colours (VisCAM and Materialise conventions) and 3MF base materials and
colour groups. Vertices are welded and, where the file has no normals,
smoothed between triangles that meet at less than 35°. A closed mesh that is
inside out is turned the right way round. OBJ is turned from Y up to Z up;
OBJ and STL have no units and are taken as millimetres.

The viewer (package `main`) flattens the tree and places every part's
vertices in world space, with a fixed studio lighting rig baked into their
colours. Each frame (`render.go`) projects them on all cores, drops triangles
that face away or are off screen, cuts those crossing the near plane, and
draws the rest. Ebitengine has no depth buffer, so the nearest surface is
found in three passes that build a 24-bit depth one byte at a time with a
"max" blend, followed by a colour pass. Picking is a CPU ray cast against the
original triangles.

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
with their reference images, and OBJ, STL and 3MF samples listed with their
licenses in [testdata/SOURCES.md](testdata/SOURCES.md). `go test ./...` loads
all of them.

## Limitations

- Vertices are projected on the CPU, and Ebitengine copies them for each of
  the four passes. A PCB with ~250k triangles takes about 8 ms per frame on
  an M1 Pro. Rendering only happens when the view changes.
- Faces whose orientation is reversed in the file are culled as back faces.
- OBJ polygons are fan triangulated, so concave ones may be filled wrongly.
  Textures are ignored, as are 3MF textures, composite materials and
  extensions other than materials and production.
- Only B-rep and faceted geometry is shown. Wireframe, PMI and tessellated
  (AP242 `TESSELLATED_*`) data are ignored.
