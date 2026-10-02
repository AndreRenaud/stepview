package step

import (
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Node is an element of the product tree.
type Node struct {
	Name     string
	Local    Affine // transform relative to the parent, in millimetres
	Mesh     *Mesh  // geometry at this node (may be nil)
	Children []*Node

	meshJob *solidJob
}

// Mesh is a triangle mesh in millimetres in its node's local frame.
type Mesh struct {
	Positions  []float32 // xyz
	Normals    []float32 // xyz
	Colors     []float32 // rgb per vertex
	Indices    []uint32
	FaceStarts []uint32 // index offsets (into Indices) where each face starts
	Bounds     Box
}

// TriangleCount returns the number of triangles.
func (m *Mesh) TriangleCount() int { return len(m.Indices) / 3 }

// Stats summarises a load.
type Stats struct {
	Entities    int
	Solids      int
	Faces       int
	FailedFaces int
	Triangles   int
	ParseTime   time.Duration
	TessTime    time.Duration
}

// Model is a loaded STEP file.
type Model struct {
	Name     string
	Roots    []*Node
	Stats    Stats
	Warnings []string
}

// Options controls loading.
type Options struct {
	// RelTolerance is the chordal tolerance relative to each solid's size.
	RelTolerance float64
	// MaxAngle is the maximum angle per segment on curved geometry.
	MaxAngle float64
	// Progress, if set, is called with a stage description and fraction.
	Progress func(stage string, frac float64)
}

// DefaultOptions returns the default load options.
func DefaultOptions() Options {
	return Options{RelTolerance: 0.001, MaxAngle: math.Pi / 10}
}

// LoadFile reads and tessellates a STEP file.
func LoadFile(path string, opt Options) (*Model, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Load(data, opt)
}

// Load parses and tessellates STEP data.
func Load(data []byte, opt Options) (*Model, error) {
	if opt.RelTolerance <= 0 {
		opt.RelTolerance = DefaultOptions().RelTolerance
	}
	if opt.MaxAngle <= 0 {
		opt.MaxAngle = DefaultOptions().MaxAngle
	}
	progress := func(s string, f float64) {
		if opt.Progress != nil {
			opt.Progress(s, f)
		}
	}
	progress("Parsing", 0)
	t0 := time.Now()
	f, err := Parse(data)
	if err != nil {
		return nil, err
	}
	l := newLoader(f, opt)
	l.model.Stats.ParseTime = time.Since(t0)
	l.model.Stats.Entities = f.Count()
	l.model.Name = f.Name
	progress("Reading structure", 0)
	l.buildTree()
	t1 := time.Now()
	l.tessellate(progress)
	l.model.Stats.TessTime = time.Since(t1)
	if len(l.model.Roots) == 0 {
		return nil, errors.New("step: no products or geometry found")
	}
	return l.model, nil
}

type loader struct {
	f     *File
	opt   Options
	geom  *geomCache
	model *Model

	pdsDef     map[int]int        // PRODUCT_DEFINITION_SHAPE -> definition
	pdReps     map[int][]int      // PRODUCT_DEFINITION -> shape representations
	repOwner   map[int]int        // representation -> PRODUCT_DEFINITION
	repLinks   map[int][]int      // plain representation relationships
	nauoByPD   map[int][]*Entity  // parent PD -> NAUOs
	nauoXform  map[int]Affine     // NAUO -> child-to-parent transform
	childPDs   map[int]bool       // PDs used as a component
	colors     map[int][3]float32 // styled item -> colour
	ctxUnits   map[int][2]float64 // context -> (mm per unit, radians per unit)
	jobs       []*solidJob
	jobByItem  map[int]*solidJob
	unitWarned bool
	nameCount  map[string]int
}

// solidJob is a geometric item (solid or shell model) to tessellate.
type solidJob struct {
	item     int
	lenScale float64
	angScale float64
	mesh     *Mesh
	name     string
}

func newLoader(f *File, opt Options) *loader {
	return &loader{
		f:         f,
		opt:       opt,
		geom:      &geomCache{f: f},
		model:     &Model{},
		pdsDef:    map[int]int{},
		pdReps:    map[int][]int{},
		repOwner:  map[int]int{},
		repLinks:  map[int][]int{},
		nauoByPD:  map[int][]*Entity{},
		nauoXform: map[int]Affine{},
		childPDs:  map[int]bool{},
		colors:    map[int][3]float32{},
		ctxUnits:  map[int][2]float64{},
		jobByItem: map[int]*solidJob{},
	}
}

func (l *loader) warnf(format string, args ...any) {
	if len(l.model.Warnings) < 200 {
		l.model.Warnings = append(l.model.Warnings, fmt.Sprintf(format, args...))
	}
}

// ---------------------------------------------------------------------------
// Units

var siPrefix = map[string]float64{
	"EXA": 1e18, "PETA": 1e15, "TERA": 1e12, "GIGA": 1e9, "MEGA": 1e6, "KILO": 1e3,
	"HECTO": 1e2, "DECA": 1e1, "DECI": 1e-1, "CENTI": 1e-2, "MILLI": 1e-3,
	"MICRO": 1e-6, "NANO": 1e-9, "PICO": 1e-12, "FEMTO": 1e-15, "ATTO": 1e-18,
}

// unitFactor returns the factor converting the unit to mm (length) or
// radians (angle), and which kind it is.
func (l *loader) unitFactor(e *Entity, depth int) (factor float64, isLength, isAngle bool) {
	if e == nil || depth > 8 {
		return 1, false, false
	}
	isLength = e.Is("LENGTH_UNIT")
	isAngle = e.Is("PLANE_ANGLE_UNIT")
	if args, ok := e.PartArgs("SI_UNIT"); ok {
		var prefix, name string
		if e.Type == "SI_UNIT" {
			// Simple form: SI_UNIT(dims, prefix, name)
			if len(args) >= 3 {
				prefix, name = args[1].Str, args[2].Str
			}
		} else if len(args) >= 2 {
			prefix, name = args[0].Str, args[1].Str
		}
		p := 1.0
		if v, ok := siPrefix[prefix]; ok {
			p = v
		}
		switch name {
		case "METRE":
			return p * 1000, true, false
		case "RADIAN":
			return p, false, true
		}
		return p, isLength, isAngle
	}
	if args, ok := e.PartArgs("CONVERSION_BASED_UNIT"); ok && len(args) >= 2 {
		m := l.f.Ref(args[1])
		if m != nil {
			margs := m.Args
			if m.Type == "" {
				for _, p := range m.Parts {
					if p.Type == "MEASURE_WITH_UNIT" {
						margs = p.Args
					}
				}
			}
			if len(margs) >= 2 {
				v := margs[0].AsFloat()
				bf, bl, ba := l.unitFactor(l.f.Ref(margs[1]), depth+1)
				name := strings.ToUpper(args[0].Str)
				if v == 0 {
					v = 1
				}
				if ba || isAngle {
					if name == "DEGREE" && math.Abs(v*bf-math.Pi/180) > 1e-6 && math.Abs(v-1) < 1e-9 {
						return math.Pi / 180, false, true
					}
					return v * bf, false, true
				}
				return v * bf, bl || isLength, false
			}
		}
	}
	return 1, isLength, isAngle
}

// contextUnits returns (mm per length unit, radians per angle unit).
func (l *loader) contextUnits(ctxID int) (float64, float64) {
	if u, ok := l.ctxUnits[ctxID]; ok {
		return u[0], u[1]
	}
	lenF, angF := 1.0, 1.0
	foundLen := false
	if e := l.f.Get(ctxID); e != nil {
		if args, ok := e.PartArgs("GLOBAL_UNIT_ASSIGNED_CONTEXT"); ok {
			units := args
			if e.Type == "GLOBAL_UNIT_ASSIGNED_CONTEXT" && len(args) >= 3 {
				units = args[2:]
			}
			if len(units) > 0 {
				for _, uv := range units[0].AsList() {
					f, isL, isA := l.unitFactor(l.f.Ref(uv), 0)
					if isL {
						lenF = f
						foundLen = true
					} else if isA {
						angF = f
					}
				}
			}
		}
	}
	if !foundLen && !l.unitWarned {
		l.unitWarned = true
		l.warnf("no length unit found for context #%d; assuming millimetres", ctxID)
	}
	l.ctxUnits[ctxID] = [2]float64{lenF, angF}
	return lenF, angF
}

// repArgs returns the items and context of a representation.
func (l *loader) repInfo(rep *Entity) ([]Value, int) {
	if rep == nil {
		return nil, 0
	}
	args := rep.Args
	if rep.Type == "" {
		if a, ok := rep.PartArgs("REPRESENTATION"); ok {
			args = a
		}
	}
	if len(args) < 3 {
		return nil, 0
	}
	return args[1].AsList(), args[2].Ref
}

func (l *loader) repUnits(repID int) (float64, float64) {
	_, ctx := l.repInfo(l.f.Get(repID))
	return l.contextUnits(ctx)
}

// placementAffine returns an AXIS2_PLACEMENT_3D as a transform, with the
// translation scaled by lenScale.
func (l *loader) placementAffine(v Value, lenScale float64) Affine {
	fr, err := l.geom.placement(v)
	if err != nil {
		return Identity()
	}
	if e := l.f.Ref(v); e != nil && e.Is("CARTESIAN_TRANSFORMATION_OPERATOR_3D") {
		return Identity()
	}
	return Frame(fr.o.Scale(lenScale), fr.x, fr.y, fr.z)
}

// transformOperator handles CARTESIAN_TRANSFORMATION_OPERATOR_3D used as a
// mapping target.
func (l *loader) transformOperator(e *Entity, lenScale float64) (Affine, bool) {
	args, ok := e.PartArgs("CARTESIAN_TRANSFORMATION_OPERATOR")
	if !ok {
		return Affine{}, false
	}
	// (name, description, axis1, axis2, local_origin, scale) for the simple
	// 3D form, plus axis3.
	if e.Type == "CARTESIAN_TRANSFORMATION_OPERATOR_3D" && len(args) >= 7 {
		x, okx := l.geom.direction(args[2])
		y, oky := l.geom.direction(args[3])
		z, okz := l.geom.direction(args[6])
		o, _ := l.geom.point(args[4])
		s := 1.0
		if args[5].Kind == KindNumber {
			s = args[5].Num
		}
		if !okz {
			z = Vec3{0, 0, 1}
		}
		z = z.Norm()
		if !okx {
			x = z.AnyPerp()
		}
		x = x.Sub(z.Scale(x.Dot(z))).Norm()
		if !oky {
			y = z.Cross(x)
		}
		y = z.Cross(x)
		a := Frame(o.Scale(lenScale), x.Scale(s), y.Scale(s), z.Scale(s))
		return a, true
	}
	return Affine{}, false
}

// ---------------------------------------------------------------------------
// Product structure

func (l *loader) productName(pdID int) string {
	pd := l.f.Get(pdID)
	if pd == nil {
		return fmt.Sprintf("#%d", pdID)
	}
	pdf := l.f.Ref(pd.Arg(2))
	if pdf != nil {
		if prod := l.f.Ref(pdf.Arg(2)); prod != nil {
			name := strings.TrimSpace(prod.Arg(1).Str)
			id := strings.TrimSpace(prod.Arg(0).Str)
			if name == "" {
				name = id
			}
			// Some exporters give every product the same name; the id is
			// then the only distinguishing label.
			if id != "" && id != name && l.productNames()[name] > 1 {
				name = id
			}
			if name != "" {
				return name
			}
		}
	}
	return fmt.Sprintf("Part #%d", pdID)
}

// productNames counts how many distinct products use each name.
func (l *loader) productNames() map[string]int {
	if l.nameCount == nil {
		l.nameCount = map[string]int{}
		for _, p := range l.f.OfType("PRODUCT") {
			l.nameCount[strings.TrimSpace(p.Arg(1).Str)]++
		}
	}
	return l.nameCount
}

func (l *loader) indexStructure() {
	f := l.f
	for _, e := range f.OfType("PRODUCT_DEFINITION_SHAPE") {
		l.pdsDef[e.ID] = e.Arg(2).Ref
	}
	for _, e := range f.OfType("PROPERTY_DEFINITION") {
		if _, ok := l.pdsDef[e.ID]; !ok && e.Type == "PROPERTY_DEFINITION" {
			l.pdsDef[e.ID] = e.Arg(2).Ref
		}
	}
	for _, e := range f.OfType("SHAPE_DEFINITION_REPRESENTATION") {
		def := l.pdsDef[e.Arg(0).Ref]
		de := f.Get(def)
		rep := e.Arg(1).Ref
		if de != nil && de.Is("PRODUCT_DEFINITION") || de != nil && strings.HasPrefix(de.Type, "PRODUCT_DEFINITION") && !de.Is("NEXT_ASSEMBLY_USAGE_OCCURRENCE") {
			l.pdReps[def] = append(l.pdReps[def], rep)
			if _, ok := l.repOwner[rep]; !ok {
				l.repOwner[rep] = def
			}
		}
	}
	for _, e := range f.OfType("NEXT_ASSEMBLY_USAGE_OCCURRENCE") {
		args, _ := e.PartArgs("PRODUCT_DEFINITION_RELATIONSHIP")
		if e.Type == "NEXT_ASSEMBLY_USAGE_OCCURRENCE" {
			args = e.Args
		}
		if len(args) < 5 {
			continue
		}
		parent, child := args[3].Ref, args[4].Ref
		l.nauoByPD[parent] = append(l.nauoByPD[parent], e)
		l.childPDs[child] = true
	}
	for _, e := range f.OfType("REPRESENTATION_RELATIONSHIP") {
		if e.Is("REPRESENTATION_RELATIONSHIP_WITH_TRANSFORMATION") {
			continue
		}
		args, ok := e.PartArgs("REPRESENTATION_RELATIONSHIP")
		if e.Type != "" {
			args, ok = e.Args, true
		}
		if !ok || len(args) < 4 {
			continue
		}
		a, b := args[2].Ref, args[3].Ref
		l.repLinks[a] = append(l.repLinks[a], b)
		l.repLinks[b] = append(l.repLinks[b], a)
	}
	for _, e := range f.OfType("SHAPE_REPRESENTATION_RELATIONSHIP") {
		if e.Type != "SHAPE_REPRESENTATION_RELATIONSHIP" {
			continue
		}
		a, b := e.Arg(2).Ref, e.Arg(3).Ref
		l.repLinks[a] = append(l.repLinks[a], b)
		l.repLinks[b] = append(l.repLinks[b], a)
	}
	for _, e := range f.OfType("CONTEXT_DEPENDENT_SHAPE_REPRESENTATION") {
		rr := f.Ref(e.Arg(0))
		nauo := l.pdsDef[e.Arg(1).Ref]
		if rr == nil || nauo == 0 {
			continue
		}
		if xf, ok := l.relationshipTransform(rr, nauo); ok {
			l.nauoXform[nauo] = xf
		}
	}
}

// relationshipTransform computes the child-to-parent transform of a
// REPRESENTATION_RELATIONSHIP_WITH_TRANSFORMATION.
func (l *loader) relationshipTransform(rr *Entity, nauoID int) (Affine, bool) {
	rargs, ok := rr.PartArgs("REPRESENTATION_RELATIONSHIP")
	if !ok || len(rargs) < 4 {
		return Affine{}, false
	}
	targs, ok := rr.PartArgs("REPRESENTATION_RELATIONSHIP_WITH_TRANSFORMATION")
	if !ok || len(targs) < 1 {
		return Affine{}, false
	}
	rep1, rep2 := rargs[2].Ref, rargs[3].Ref
	// rep_1 is normally the child. Some writers swap them; detect that by
	// checking which representation belongs to the parent.
	reversed := false
	if nauo := l.f.Get(nauoID); nauo != nil {
		parent := nauo.Arg(3).Ref
		if l.repOwner[rep1] == parent && l.repOwner[rep2] != parent {
			reversed = true
		}
	}
	idt := l.f.Ref(targs[0])
	if idt == nil {
		return Affine{}, false
	}
	l1, _ := l.repUnits(rep1)
	l2, _ := l.repUnits(rep2)
	var m1, m2 Affine
	if idt.Is("ITEM_DEFINED_TRANSFORMATION") {
		m1 = l.placementAffine(idt.Arg(2), l1)
		m2 = l.placementAffine(idt.Arg(3), l2)
	} else if a, ok := l.transformOperator(idt, l2); ok {
		if reversed {
			return a.Inverse(), true
		}
		return a, true
	} else {
		return Affine{}, false
	}
	xf := m2.Mul(m1.Inverse())
	if reversed {
		xf = xf.Inverse()
	}
	return xf, true
}

// shapeItem is a geometric item found in a representation, with the
// transform from its representation to the product's frame.
type shapeItem struct {
	id    int
	xf    Affine
	rep   int
	child int // for mapped items referencing another product
}

// gatherShapes collects geometric items reachable from a product's
// representations. Mapped items pointing at other products' representations
// are returned separately.
func (l *loader) gatherShapes(pd int) (items []shapeItem, mappedChildren []shapeItem) {
	visited := map[int]bool{}
	type entry struct {
		rep   int
		xf    Affine
		depth int
	}
	var queue []entry
	for _, r := range l.pdReps[pd] {
		queue = append(queue, entry{r, Identity(), 0})
	}
	for len(queue) > 0 {
		en := queue[0]
		queue = queue[1:]
		key := en.rep
		if visited[key] && en.depth == 0 {
			continue
		}
		visited[key] = true
		rep := l.f.Get(en.rep)
		repItems, _ := l.repInfo(rep)
		lenScale, _ := l.repUnits(en.rep)
		for _, iv := range repItems {
			it := l.f.Ref(iv)
			if it == nil {
				continue
			}
			switch {
			case it.Is("MANIFOLD_SOLID_BREP") || it.Is("BREP_WITH_VOIDS") || it.Is("FACETED_BREP") ||
				it.Is("SHELL_BASED_SURFACE_MODEL") || it.Is("FACE_BASED_SURFACE_MODEL") ||
				it.Is("CLOSED_SHELL") || it.Is("OPEN_SHELL"):
				items = append(items, shapeItem{id: it.ID, xf: en.xf, rep: en.rep})
			case it.Is("MAPPED_ITEM") && en.depth < 16:
				src := l.f.Ref(it.Arg(1))
				if src == nil {
					continue
				}
				mrep := src.Arg(1).Ref
				mLen, _ := l.repUnits(mrep)
				origin := l.placementAffine(src.Arg(0), mLen)
				var target Affine
				if te := l.f.Ref(it.Arg(2)); te != nil && te.Is("CARTESIAN_TRANSFORMATION_OPERATOR") {
					target, _ = l.transformOperator(te, lenScale)
				} else {
					target = l.placementAffine(it.Arg(2), lenScale)
				}
				xf := en.xf.Mul(target.Mul(origin.Inverse()))
				if owner, ok := l.repOwner[mrep]; ok && owner != pd {
					mappedChildren = append(mappedChildren, shapeItem{id: it.ID, xf: xf, rep: mrep, child: owner})
					continue
				}
				queue = append(queue, entry{mrep, xf, en.depth + 1})
			}
		}
		if en.depth == 0 {
			for _, linked := range l.repLinks[en.rep] {
				if visited[linked] {
					continue
				}
				if owner, ok := l.repOwner[linked]; ok && owner != pd {
					continue
				}
				queue = append(queue, entry{linked, en.xf, 0})
			}
		}
	}
	return items, mappedChildren
}

func (l *loader) job(item shapeItem) *solidJob {
	if j, ok := l.jobByItem[item.id]; ok {
		return j
	}
	ls, as := l.repUnits(item.rep)
	e := l.f.Get(item.id)
	name := ""
	if e != nil && len(e.Args) > 0 {
		name = strings.TrimSpace(e.Args[0].Str)
	}
	j := &solidJob{item: item.id, lenScale: ls, angScale: as, name: name}
	l.jobByItem[item.id] = j
	l.jobs = append(l.jobs, j)
	return j
}

func (l *loader) buildTree() {
	l.indexStructure()
	l.collectColors()
	var roots []int
	for _, e := range l.f.OfType("PRODUCT_DEFINITION") {
		if l.childPDs[e.ID] {
			continue
		}
		if len(l.pdReps[e.ID]) == 0 && len(l.nauoByPD[e.ID]) == 0 {
			continue
		}
		roots = append(roots, e.ID)
	}
	for _, r := range roots {
		n := l.buildNode(r, 0, map[int]bool{})
		if n != nil {
			l.model.Roots = append(l.model.Roots, n)
		}
	}
	if len(l.model.Roots) == 0 {
		// No product structure: show every solid at the top level.
		root := &Node{Name: l.f.Name, Local: Identity()}
		if root.Name == "" {
			root.Name = "Model"
		}
		for _, t := range []string{"MANIFOLD_SOLID_BREP", "BREP_WITH_VOIDS", "FACETED_BREP", "SHELL_BASED_SURFACE_MODEL"} {
			for _, e := range l.f.OfType(t) {
				rep := 0
				for _, r := range l.f.OfType("SHAPE_REPRESENTATION") {
					items, _ := l.repInfo(r)
					for _, iv := range items {
						if iv.Ref == e.ID {
							rep = r.ID
						}
					}
				}
				j := l.job(shapeItem{id: e.ID, xf: Identity(), rep: rep})
				root.Children = append(root.Children, &Node{Name: l.solidName(j, len(root.Children)), Local: Identity(), meshJob: j})
			}
		}
		if len(root.Children) > 0 {
			l.model.Roots = append(l.model.Roots, root)
		}
	}
}

func (l *loader) solidName(j *solidJob, i int) string {
	if j.name != "" && j.name != "NONE" {
		return j.name
	}
	return fmt.Sprintf("Solid %d", i+1)
}

func isIdentity(a Affine) bool {
	id := Identity()
	for i := range 3 {
		for k := range 3 {
			if math.Abs(a.R[i][k]-id.R[i][k]) > 1e-12 {
				return false
			}
		}
	}
	return a.T.Len() < 1e-12
}

func (l *loader) buildNode(pd int, depth int, path map[int]bool) *Node {
	if depth > 64 || path[pd] {
		return nil
	}
	path[pd] = true
	defer delete(path, pd)
	n := &Node{Name: l.productName(pd), Local: Identity()}
	items, mapped := l.gatherShapes(pd)
	if len(items) == 1 && isIdentity(items[0].xf) && len(l.nauoByPD[pd]) == 0 && len(mapped) == 0 {
		n.meshJob = l.job(items[0])
	} else {
		for i, it := range items {
			j := l.job(it)
			n.Children = append(n.Children, &Node{Name: l.solidName(j, i), Local: it.xf, meshJob: j})
		}
	}
	// Components.
	used := make([]bool, len(mapped))
	for _, nauo := range l.nauoByPD[pd] {
		args := nauo.Args
		if nauo.Type == "" {
			args, _ = nauo.PartArgs("PRODUCT_DEFINITION_RELATIONSHIP")
		}
		if len(args) < 5 {
			continue
		}
		child := args[4].Ref
		xf, ok := l.nauoXform[nauo.ID]
		if !ok {
			// Mapped-item style assembly: pair with a mapped item.
			xf = Identity()
			for i, m := range mapped {
				if !used[i] && m.child == child {
					used[i] = true
					xf = m.xf
					break
				}
			}
		}
		cn := l.buildNode(child, depth+1, path)
		if cn == nil {
			continue
		}
		cn.Local = xf.Mul(cn.Local)
		n.Children = append(n.Children, cn)
	}
	for i, m := range mapped {
		if used[i] {
			continue
		}
		cn := l.buildNode(m.child, depth+1, path)
		if cn == nil {
			continue
		}
		cn.Local = m.xf.Mul(cn.Local)
		n.Children = append(n.Children, cn)
	}
	if n.meshJob == nil && len(n.Children) == 0 {
		return nil
	}
	return n
}

// ---------------------------------------------------------------------------
// Colours

var predefinedColours = map[string][3]float32{
	"red": {1, 0, 0}, "green": {0, 1, 0}, "blue": {0, 0, 1}, "yellow": {1, 1, 0},
	"magenta": {1, 0, 1}, "cyan": {0, 1, 1}, "black": {0.05, 0.05, 0.05}, "white": {1, 1, 1},
}

func (l *loader) colour(e *Entity, depth int) ([3]float32, bool) {
	if e == nil || depth > 10 {
		return [3]float32{}, false
	}
	if args, ok := e.PartArgs("COLOUR_RGB"); ok {
		if e.Type == "COLOUR_RGB" && len(args) >= 4 {
			return [3]float32{float32(args[1].AsFloat()), float32(args[2].AsFloat()), float32(args[3].AsFloat())}, true
		}
		if len(args) >= 3 {
			return [3]float32{float32(args[0].AsFloat()), float32(args[1].AsFloat()), float32(args[2].AsFloat())}, true
		}
	}
	if e.Is("DRAUGHTING_PRE_DEFINED_COLOUR") {
		args, _ := e.PartArgs("PRE_DEFINED_ITEM")
		if e.Type == "DRAUGHTING_PRE_DEFINED_COLOUR" {
			args = e.Args
		}
		if len(args) > 0 {
			if c, ok := predefinedColours[strings.ToLower(args[0].Str)]; ok {
				return c, true
			}
		}
	}
	return [3]float32{}, false
}

// surfaceStyleColour digs a surface colour out of a presentation style.
func (l *loader) styleColour(v Value, depth int, surfaceOnly bool) ([3]float32, bool) {
	if depth > 12 {
		return [3]float32{}, false
	}
	if v.Kind == KindList {
		for _, x := range v.List {
			if c, ok := l.styleColour(x, depth+1, surfaceOnly); ok {
				return c, true
			}
		}
		return [3]float32{}, false
	}
	e := l.f.Ref(v)
	if e == nil {
		return [3]float32{}, false
	}
	switch e.Type {
	case "PRESENTATION_STYLE_ASSIGNMENT", "PRESENTATION_STYLE_BY_CONTEXT":
		return l.styleColour(e.Arg(0), depth+1, surfaceOnly)
	case "SURFACE_STYLE_USAGE":
		return l.styleColour(e.Arg(1), depth+1, surfaceOnly)
	case "SURFACE_SIDE_STYLE":
		return l.styleColour(e.Arg(1), depth+1, surfaceOnly)
	case "SURFACE_STYLE_FILL_AREA":
		return l.styleColour(e.Arg(0), depth+1, surfaceOnly)
	case "FILL_AREA_STYLE":
		return l.styleColour(e.Arg(1), depth+1, surfaceOnly)
	case "FILL_AREA_STYLE_COLOUR":
		return l.colour(l.f.Ref(e.Arg(1)), depth+1)
	case "SURFACE_STYLE_RENDERING", "SURFACE_STYLE_RENDERING_WITH_PROPERTIES":
		return l.colour(l.f.Ref(e.Arg(1)), depth+1)
	case "CURVE_STYLE":
		if surfaceOnly {
			return [3]float32{}, false
		}
		return l.colour(l.f.Ref(e.Arg(3)), depth+1)
	}
	return [3]float32{}, false
}

func (l *loader) collectColors() {
	apply := func(e *Entity) {
		args := e.Args
		if e.Type == "" {
			args, _ = e.PartArgs("STYLED_ITEM")
		}
		if len(args) < 3 {
			return
		}
		c, ok := l.styleColour(args[1], 0, true)
		if !ok {
			return
		}
		l.colors[args[2].Ref] = c
	}
	for _, e := range l.f.OfType("STYLED_ITEM") {
		apply(e)
	}
	// Overriding styles take precedence.
	for _, e := range l.f.OfType("OVER_RIDING_STYLED_ITEM") {
		apply(e)
	}
}

// ---------------------------------------------------------------------------
// Tessellation

type faceTask struct {
	job   int
	face  int
	flip  bool
	color [3]float32
	mesh  *faceMesh
}

type edgeTask struct {
	id       int
	tol      float64
	maxAngle float64
	pts      []Vec3
	done     bool
}

var defaultColour = [3]float32{0.72, 0.73, 0.76}

func (l *loader) tessellate(progress func(string, float64)) {
	var faces []*faceTask
	edgeIdx := map[int]int{}
	var edges []*edgeTask
	jobTol := make([]tessParams, len(l.jobs))
	jobFaces := make([][]int, len(l.jobs))
	jobSize := make([]float64, len(l.jobs))

	for ji, j := range l.jobs {
		e := l.f.Get(j.item)
		base := defaultColour
		if c, ok := l.colors[j.item]; ok {
			base = c
		}
		var shellFaces []*faceTask
		var shells []int
		var flips []bool
		switch {
		case e == nil:
		case e.Is("MANIFOLD_SOLID_BREP") || e.Is("FACETED_BREP"):
			shells = append(shells, e.Arg(1).Ref)
			flips = append(flips, false)
			if e.Is("BREP_WITH_VOIDS") {
				args, _ := e.PartArgs("BREP_WITH_VOIDS")
				if e.Type == "BREP_WITH_VOIDS" && len(args) >= 3 {
					args = args[2:]
				}
				if len(args) > 0 {
					for _, v := range args[0].AsList() {
						shells = append(shells, v.Ref)
						flips = append(flips, false)
					}
				}
			}
		case e.Is("BREP_WITH_VOIDS"):
			shells = append(shells, e.Arg(1).Ref)
			flips = append(flips, false)
			for _, v := range e.Arg(2).AsList() {
				shells = append(shells, v.Ref)
				flips = append(flips, false)
			}
		case e.Is("SHELL_BASED_SURFACE_MODEL"):
			for _, v := range e.Arg(1).AsList() {
				shells = append(shells, v.Ref)
				flips = append(flips, false)
			}
		case e.Is("FACE_BASED_SURFACE_MODEL"):
			for _, v := range e.Arg(1).AsList() {
				shells = append(shells, v.Ref)
				flips = append(flips, false)
			}
		case e.Is("CLOSED_SHELL") || e.Is("OPEN_SHELL"):
			shells = append(shells, e.ID)
			flips = append(flips, false)
		}
		box := EmptyBox()
		for si, sid := range shells {
			sh := l.f.Get(sid)
			flip := flips[si]
			if sh != nil && sh.Is("ORIENTED_CLOSED_SHELL") || sh != nil && sh.Is("ORIENTED_OPEN_SHELL") {
				if !sh.Arg(3).AsBool() {
					flip = !flip
				}
				sid = sh.Arg(2).Ref
				sh = l.f.Get(sid)
			}
			if sh == nil {
				continue
			}
			shellColour := base
			if c, ok := l.colors[sid]; ok {
				shellColour = c
			}
			for _, fv := range sh.Arg(1).AsList() {
				fe := l.f.Ref(fv)
				if fe == nil {
					continue
				}
				fflip := flip
				if fe.Is("ORIENTED_FACE") {
					if !fe.Arg(3).AsBool() {
						fflip = !fflip
					}
					fe = l.f.Ref(fe.Arg(2))
					if fe == nil {
						continue
					}
				}
				col := shellColour
				if c, ok := l.colors[fe.ID]; ok {
					col = c
				}
				ft := &faceTask{job: ji, face: fe.ID, flip: fflip, color: col}
				shellFaces = append(shellFaces, ft)
				l.scanFace(fe, &box, edgeIdx, &edges)
			}
		}
		jobSize[ji] = box.Diag()
		for _, ft := range shellFaces {
			jobFaces[ji] = append(jobFaces[ji], len(faces))
			faces = append(faces, ft)
		}
	}
	// Solids without a usable size fall back to the largest solid's size.
	maxSize := 0.0
	for _, s := range jobSize {
		if s > 0 && !math.IsInf(s, 0) && !math.IsNaN(s) {
			maxSize = math.Max(maxSize, s)
		}
	}
	if maxSize == 0 {
		maxSize = 1
	}
	for ji, size := range jobSize {
		if !(size > 0) || math.IsInf(size, 0) {
			size = maxSize
		}
		// Small parts get a coarser tolerance and angle limit relative to
		// their size so that, e.g., hundreds of solder balls do not dominate
		// the triangle budget.
		tol := math.Max(size*l.opt.RelTolerance, maxSize*l.opt.RelTolerance*0.2)
		ang := l.opt.MaxAngle
		if ratio := size / maxSize; ratio < 0.05 {
			ang = math.Min(math.Pi/4, l.opt.MaxAngle*math.Sqrt(0.05/ratio))
		}
		jobTol[ji] = tessParams{tol: tol, maxAngle: ang}
	}
	// Assign tolerances to edges from the first job using them.
	for _, ft := range faces {
		fe := l.f.Get(ft.face)
		l.forEachEdge(fe, func(id int) {
			if k, ok := edgeIdx[id]; ok && edges[k].tol == 0 {
				edges[k].tol = jobTol[ft.job].tol
				edges[k].maxAngle = jobTol[ft.job].maxAngle
			}
		})
	}
	l.model.Stats.Faces = len(faces)
	l.model.Stats.Solids = len(l.jobs)

	workers := runtime.GOMAXPROCS(0)
	total := float64(len(edges) + len(faces))
	var doneCount atomic.Int64
	report := func() {
		n := doneCount.Add(1)
		if n%256 == 0 {
			progress("Tessellating", float64(n)/math.Max(total, 1))
		}
	}
	parallel(len(edges), workers, func(i int) {
		et := edges[i]
		func() {
			defer func() { recover() }()
			et.pts = l.sampleEdge(et.id, tessParams{tol: et.tol, maxAngle: et.maxAngle})
		}()
		report()
	})
	edgePts := make(map[int][]Vec3, len(edges))
	for _, et := range edges {
		edgePts[et.id] = et.pts
	}
	var failed atomic.Int64
	var warnMu sync.Mutex
	parallel(len(faces), workers, func(i int) {
		ft := faces[i]
		fm, err := func() (fm *faceMesh, err error) {
			// A bug triggered by one entity must not bring the viewer down.
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panic: %v", r)
				}
			}()
			return l.tessFace(ft, edgePts, jobTol[ft.job])
		}()
		if errors.Is(err, errEmptyFace) {
			// Zero-area faces simply produce no triangles.
		} else if err != nil {
			failed.Add(1)
			warnMu.Lock()
			l.warnf("face #%d: %v", ft.face, err)
			warnMu.Unlock()
		} else {
			ft.mesh = fm
		}
		report()
	})
	l.model.Stats.FailedFaces = int(failed.Load())

	// Assemble meshes per job.
	for ji, j := range l.jobs {
		m := &Mesh{Bounds: EmptyBox()}
		s := j.lenScale
		for _, fi := range jobFaces[ji] {
			ft := faces[fi]
			if ft.mesh == nil {
				continue
			}
			base := uint32(len(m.Positions) / 3)
			m.FaceStarts = append(m.FaceStarts, uint32(len(m.Indices)))
			for k, p := range ft.mesh.pos {
				ps := p.Scale(s)
				m.Bounds.Extend(ps)
				n := ft.mesh.nrm[k]
				if ft.flip {
					n = n.Scale(-1)
				}
				m.Positions = append(m.Positions, float32(ps.X), float32(ps.Y), float32(ps.Z))
				m.Normals = append(m.Normals, float32(n.X), float32(n.Y), float32(n.Z))
				m.Colors = append(m.Colors, ft.color[0], ft.color[1], ft.color[2])
			}
			t := ft.mesh.tris
			for k := 0; k+2 < len(t); k += 3 {
				a, b, c := uint32(t[k])+base, uint32(t[k+1])+base, uint32(t[k+2])+base
				if ft.flip {
					b, c = c, b
				}
				m.Indices = append(m.Indices, a, b, c)
			}
		}
		l.model.Stats.Triangles += len(m.Indices) / 3
		j.mesh = m
	}
	var attach func(n *Node)
	attach = func(n *Node) {
		if n.meshJob != nil {
			n.Mesh = n.meshJob.mesh
			n.meshJob = nil
		}
		for _, c := range n.Children {
			attach(c)
		}
	}
	for _, r := range l.model.Roots {
		attach(r)
	}
	progress("Done", 1)
}

func parallel(n, workers int, fn func(i int)) {
	if n == 0 {
		return
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < min(workers, n); w++ {
		wg.Go(func() {
			for {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				fn(i)
			}
		})
	}
	wg.Wait()
}

// forEachEdge calls fn for each EDGE_CURVE id used by a face.
func (l *loader) forEachEdge(fe *Entity, fn func(id int)) {
	if fe == nil {
		return
	}
	for _, bv := range fe.Arg(1).AsList() {
		b := l.f.Ref(bv)
		if b == nil {
			continue
		}
		loop := l.f.Ref(b.Arg(1))
		if loop == nil || !loop.Is("EDGE_LOOP") {
			continue
		}
		for _, oev := range loop.Arg(1).AsList() {
			oe := l.f.Ref(oev)
			if oe == nil {
				continue
			}
			if oe.Is("ORIENTED_EDGE") {
				fn(oe.Arg(3).Ref)
			} else if oe.Is("EDGE_CURVE") {
				fn(oe.ID)
			}
		}
	}
}

// scanFace registers a face's edges and extends the bounding box.
func (l *loader) scanFace(fe *Entity, box *Box, edgeIdx map[int]int, edges *[]*edgeTask) {
	// Closed surfaces may have no edges at all, so include their extent.
	if se := l.f.Ref(fe.Arg(2)); se != nil && (se.Type == "SPHERICAL_SURFACE" || se.Type == "TOROIDAL_SURFACE") {
		if fr, err := l.geom.placement(se.Arg(1)); err == nil {
			r := se.Arg(2).AsFloat()
			if se.Type == "TOROIDAL_SURFACE" {
				r += se.Arg(3).AsFloat()
			}
			box.Extend(fr.o.Add(Vec3{r, r, r}))
			box.Extend(fr.o.Sub(Vec3{r, r, r}))
		}
	}
	for _, bv := range fe.Arg(1).AsList() {
		b := l.f.Ref(bv)
		if b == nil {
			continue
		}
		loop := l.f.Ref(b.Arg(1))
		if loop == nil {
			continue
		}
		switch {
		case loop.Is("POLY_LOOP"):
			for _, pv := range loop.Arg(1).AsList() {
				if p, err := l.geom.point(pv); err == nil {
					box.Extend(p)
				}
			}
		case loop.Is("EDGE_LOOP"):
			for _, oev := range loop.Arg(1).AsList() {
				oe := l.f.Ref(oev)
				if oe == nil {
					continue
				}
				ec := oe
				if oe.Is("ORIENTED_EDGE") {
					ec = l.f.Ref(oe.Arg(3))
				}
				if ec == nil {
					continue
				}
				if _, ok := edgeIdx[ec.ID]; !ok {
					edgeIdx[ec.ID] = len(*edges)
					*edges = append(*edges, &edgeTask{id: ec.ID})
				}
				for _, vi := range []int{1, 2} {
					if p, err := l.geom.point(ec.Arg(vi)); err == nil {
						box.Extend(p)
					}
				}
			}
		case loop.Is("VERTEX_LOOP"):
			if p, err := l.geom.point(loop.Arg(1)); err == nil {
				box.Extend(p)
			}
		}
	}
}

// sampleEdge returns the polyline of an EDGE_CURVE from its start vertex to
// its end vertex.
func (l *loader) sampleEdge(id int, prm tessParams) []Vec3 {
	ec := l.f.Get(id)
	if ec == nil {
		return nil
	}
	p0, err0 := l.geom.point(ec.Arg(1))
	p1, err1 := l.geom.point(ec.Arg(2))
	if err0 != nil || err1 != nil {
		return nil
	}
	c, err := l.geom.curve(ec.Arg(3).Ref)
	if err != nil {
		return []Vec3{p0, p1}
	}
	sense := ec.Arg(4).Kind != KindEnum || ec.Arg(4).AsBool()
	closed := p0.Dist(p1) <= prm.tol*1e-2
	a, b := p0, p1
	if !sense {
		a, b = p1, p0
	}
	ta, tb := c.Project(a), c.Project(b)
	if period := c.Period(); period > 0 {
		if closed {
			tb = ta + period
		} else {
			for tb <= ta {
				tb += period
			}
			for tb > ta+period {
				tb -= period
			}
		}
	} else if closed {
		ta, tb = c.Range()
		if math.IsInf(ta, 0) || math.IsInf(tb, 0) {
			return []Vec3{p0, p1}
		}
		if !sense {
			ta, tb = tb, ta
		}
		ta, tb = math.Min(ta, tb), math.Max(ta, tb)
	}
	ts := sampleCurveParams(c, ta, tb, prm.tol, prm.maxAngle)
	pts := make([]Vec3, len(ts))
	for i, t := range ts {
		pts[i], _ = c.EvalD(t)
	}
	pts[0] = a
	pts[len(pts)-1] = b
	if !sense {
		for i, k := 0, len(pts)-1; i < k; i, k = i+1, k-1 {
			pts[i], pts[k] = pts[k], pts[i]
		}
	}
	return pts
}

// tessFace builds the boundary loops of a face and tessellates it.
func (l *loader) tessFace(ft *faceTask, edgePts map[int][]Vec3, prm tessParams) (*faceMesh, error) {
	fe := l.f.Get(ft.face)
	if fe == nil {
		return nil, errors.New("missing face")
	}
	_, angScale := 1.0, l.jobs[ft.job].angScale
	var loops [][]Vec3
	for _, bv := range fe.Arg(1).AsList() {
		b := l.f.Ref(bv)
		if b == nil {
			continue
		}
		loop := l.f.Ref(b.Arg(1))
		if loop == nil {
			continue
		}
		var pts []Vec3
		switch {
		case loop.Is("POLY_LOOP"):
			for _, pv := range loop.Arg(1).AsList() {
				if p, err := l.geom.point(pv); err == nil {
					pts = append(pts, p)
				}
			}
		case loop.Is("EDGE_LOOP"):
			for _, oev := range loop.Arg(1).AsList() {
				oe := l.f.Ref(oev)
				if oe == nil {
					continue
				}
				ec := oe
				orient := true
				if oe.Is("ORIENTED_EDGE") {
					ec = l.f.Ref(oe.Arg(3))
					orient = oe.Arg(4).AsBool()
				}
				if ec == nil {
					continue
				}
				ep := edgePts[ec.ID]
				if len(ep) == 0 {
					continue
				}
				if orient {
					for k, p := range ep {
						if k == 0 && len(pts) > 0 && pts[len(pts)-1].Dist(p) < prm.tol*1e-3 {
							continue
						}
						pts = append(pts, p)
					}
				} else {
					for k, p := range slices.Backward(ep) {

						if k == len(ep)-1 && len(pts) > 0 && pts[len(pts)-1].Dist(p) < prm.tol*1e-3 {
							continue
						}
						pts = append(pts, p)
					}
				}
			}
		case loop.Is("VERTEX_LOOP"):
			continue
		}
		if len(b.Args) >= 3 && b.Arg(2).Kind == KindEnum && !b.Arg(2).AsBool() {
			for i, k := 0, len(pts)-1; i < k; i, k = i+1, k-1 {
				pts[i], pts[k] = pts[k], pts[i]
			}
		}
		if len(pts) >= 2 {
			loops = append(loops, pts)
		}
	}
	sense := true
	var surf Surface
	switch {
	case fe.Is("ADVANCED_FACE") || fe.Is("FACE_SURFACE"):
		var err error
		surf, err = l.geom.surface(fe.Arg(2).Ref, angScale)
		if err != nil {
			surf = nil
		}
		sense = fe.Arg(3).Kind != KindEnum || fe.Arg(3).AsBool()
	}
	if surf == nil {
		if len(loops) == 0 {
			return nil, errors.New("face has no usable boundary")
		}
		// Faceted face (or unsupported surface): triangulate the loops in
		// their best-fit plane.
		return tessellateProjected(&planeSurface{f: frame{x: Vec3{1, 0, 0}, y: Vec3{0, 1, 0}, z: Vec3{0, 0, 1}}}, true, loops, prm)
	}
	return tessellateFace(surf, sense, loops, prm)
}
