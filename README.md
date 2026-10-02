# stepview

A STEP (ISO 10303-21) model viewer written in Go, using
[guigui](https://github.com/guigui-gui/guigui) for the UI,
[tetra3d](https://github.com/SolarLune/tetra3d) for 3D rendering and
[sqweek/dialog](https://github.com/sqweek/dialog) for the native file dialog.
glTF (`.gltf`/`.glb`) files can be opened too.

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
  On macOS the app bundle also opens STEP and glTF files double-clicked in
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
  glTF meshes), with the selection drawn in orange.

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

The viewer (package `main`) flattens the tree and groups part geometry into
tetra3d meshes. tetra3d depth-tests between mesh parts but only sorts the
triangles inside one, and every part costs several full-screen passes. So
parts share a mesh only when their bounding boxes don't intersect, which keeps
nested and touching parts depth-correct while keeping the draw count low.
Lighting is a fixed studio rig baked into vertex colours. Picking is a CPU ray
cast against the original triangles.

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
with their reference images. `go test ./...` loads all of them.

## Limitations

- tetra3d transforms, sorts and rasterises every triangle on the CPU each
  frame. Models up to roughly 100k triangles stay interactive; a dense PCB
  with ~250k triangles renders at about 15 FPS while rotating. Rendering only
  happens when the view changes.
- Inside a single part, triangles are depth-sorted rather than depth-buffered.
  This can show small artefacts on strongly concave parts.
- Only B-rep and faceted geometry is shown. Wireframe, PMI and tessellated
  (AP242 `TESSELLATED_*`) data are ignored.
