# Test data sources

The `.stp` files and their reference images come from the
[STEP Tools sample collection](https://www.steptools.com/docs/stpfiles/ap203/index.html).

The mesh samples are listed below. They are unmodified apart from renaming,
`cornell_box.obj`'s `mtllib` line was changed to match its renamed
material library, and the changes noted in the table. Textured samples with
separate texture files are kept in a directory each.

| File | Notes | Source | Author | License |
| --- | --- | --- | --- | --- |
| `cornell_box.obj`, `cornell_box.mtl` | groups, materials, quads, negative indices, CRLF | [McGuire Computer Graphics Archive](https://casual-effects.com/data/) (`CornellBox-Original`) | Guedis Cardenas and Morgan McGuire | CC BY 3.0 |
| `spot_control.obj` | quads and pentagons, texture coordinates | [Keenan's 3D Model Repository](https://www.cs.cmu.edu/~kmcrane/Projects/ModelRepository/) (`spot_control_mesh.obj`) | Keenan Crane | CC0 |
| `bob_control.obj` | `v/vt/vn` faces; refers to a material library that is not present | [Keenan's 3D Model Repository](https://www.cs.cmu.edu/~kmcrane/Projects/ModelRepository/) (`bob_controlmesh.obj`) | Keenan Crane | CC0 |
| `spur_gear.stl` | binary, Materialise `COLOR=` header | [Wikimedia Commons](https://commons.wikimedia.org/wiki/File:N%3D41,_P%3D28_-_Spur_Gear_v11_(Hatch,_Crew,_Apollo_11_by_NASA_and_Smithsonian_Institution).stl) | NASA / Smithsonian Institution | CC0 |
| `backplate_clamp.stl` | ASCII, inside out, T-junctions | [Wikimedia Commons](https://commons.wikimedia.org/wiki/File:486-case-mini-backplate-clamp-rev1.stl) | wiretap | CC BY 4.0 |
| `half_donut_solid_header.stl` | binary with a header starting `solid`, one trailing byte | [numpy-stl](https://github.com/wolph/numpy-stl/blob/develop/tests/stl_binary/HalfDonut.stl) | Rick van Hattem | BSD-3-Clause (below) |
| `box.3mf` | single mesh | [3mf-samples](https://github.com/3MFConsortium/3mf-samples/blob/master/examples/core/box.3mf) | 3MF Consortium | BSD-2-Clause (below) |
| `multiple_cylinders.3mf` | base materials, one mesh in six build items | [3mf-samples](https://github.com/3MFConsortium/3mf-samples/blob/master/examples/core/multiple_cylinders.3mf) | 3MF Consortium | BSD-2-Clause (below) |
| `components.3mf` | components with non-uniform transforms | [3mf-samples](https://github.com/3MFConsortium/3mf-samples/blob/master/validation%20tests/_archive/3mf-Verify/MUSTPASS/MUSTPASS_Chapter4.2_Components.3mf) | 3MF Consortium | BSD-2-Clause (below) |
| `rhombicuboctahedron_color.3mf` | materials extension colour group | [3mf-samples](https://github.com/3MFConsortium/3mf-samples/blob/master/examples/material/rhombicuboctahedron_color.3mf) | 3MF Consortium | BSD-2-Clause (below) |
| `fels.3ds` | one material, no smoothing groups | [assimp](https://github.com/assimp/assimp/blob/master/test/models/3DS/fels.3ds) | assimp team | BSD-3-Clause (below) |
| `room.3ds` | nine boxes, materials, smoothing groups | [assimp](https://github.com/assimp/assimp/blob/master/test/models/3DS/test1.3ds) (`test1.3ds`) | assimp team | BSD-3-Clause (below) |
| `cubes_with_alpha.3ds` | five materials | [assimp](https://github.com/assimp/assimp/blob/master/test/models/3DS/cubes_with_alpha.3DS) | assimp team | BSD-3-Clause (below) |
| `camera_child_object.3ds` | keyframer hierarchy: a box parented to a camera | [assimp](https://github.com/assimp/assimp/blob/master/test/models/3DS/CameraRollAnimWithChildObject.3ds) | assimp team | BSD-3-Clause (below) |
| `spot_textured/spot.obj`, `spot.mtl`, `spot_texture.png` | texture coordinates and a `map_Kd` texture. `spot.obj` is `spot_triangulated.obj` with `mtllib spot.mtl` and `usemtl spot` lines added at the top; `spot.mtl` was written for it. | [Keenan's 3D Model Repository](https://www.cs.cmu.edu/~kmcrane/Projects/ModelRepository/) (`spot.zip`) | Keenan Crane | CC0 (public domain dedication in the archive's README) |
| `cube_textured/cube_with_diffuse_texture.3ds`, `test.png` | texture map and UVs; refers to the image as `TEST.PNG` | [assimp](https://github.com/assimp/assimp/blob/master/test/models/3DS/cube_with_diffuse_texture.3DS), [`test.png`](https://github.com/assimp/assimp/blob/master/test/models/3DS/test.png) | assimp team | BSD-3-Clause (below). `test.png` is the assimp team's own labelled test image; the folder's `textures.txt` credit to cgtextures.com is for its photographic textures, which are not included. |
| `sphere_logo.3mf` | materials extension `texture2d` and `texture2dgroup`, mixed with a colour group | [3mf-samples](https://github.com/3MFConsortium/3mf-samples/blob/master/examples/material/sphere_logo.3mf) | 3MF Consortium | BSD-2-Clause (below) |
| `texture_coordinate_test.glb` | base colour texture embedded in a buffer view; checks UV orientation | [glTF-Sample-Assets](https://github.com/KhronosGroup/glTF-Sample-Assets/tree/main/Models/TextureCoordinateTest) (`glTF-Binary/TextureCoordinateTest.glb`) | Analytical Graphics, Inc. (Ed Mackey) | CC0 |
| `avocado/Avocado.gltf`, `Avocado.bin`, `Avocado_*.png` | external base colour, normal and metallic-roughness images. The images were scaled down from 2048 px to 512 px (base colour) and 256 px (the others) to keep the repository small. | [glTF-Sample-Assets](https://github.com/KhronosGroup/glTF-Sample-Assets/tree/main/Models/Avocado) (`glTF/`) | Microsoft | CC0 |

## 3mf-samples license

```
BSD 2-Clause License

Copyright (c) 2018, 3MF Consortium
All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

* Redistributions of source code must retain the above copyright notice, this
  list of conditions and the following disclaimer.

* Redistributions in binary form must reproduce the above copyright notice,
  this list of conditions and the following disclaimer in the documentation
  and/or other materials provided with the distribution.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE
FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER
CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY,
OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

## assimp license

```
Copyright (c) 2006-2026, assimp team
All rights reserved.

Redistribution and use of this software in source and binary forms,
with or without modification, are permitted provided that the
following conditions are met:

* Redistributions of source code must retain the above
  copyright notice, this list of conditions and the
  following disclaimer.

* Redistributions in binary form must reproduce the above
  copyright notice, this list of conditions and the
  following disclaimer in the documentation and/or other
  materials provided with the distribution.

* Neither the name of the assimp team, nor the names of its
  contributors may be used to endorse or promote products
  derived from this software without specific prior
  written permission of the assimp team.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

## numpy-stl license

```
Copyright (c) 2016, Rick van Hattem <wolph@wol.ph> All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice, this
list of conditions and the following disclaimer.

2. Redistributions in binary form must reproduce the above copyright notice,
    this list of conditions and the following disclaimer in the documentation
    and/or other materials provided with the distribution.

3. Neither the name of the copyright holder nor the names of its contributors
may be used to endorse or promote products derived from this software without
specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS" AND
ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED
WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE
FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER
CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY,
OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```
