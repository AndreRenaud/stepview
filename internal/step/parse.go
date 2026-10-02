// Package step reads ISO 10303-21 (STEP) files, extracts the product
// structure and tessellates the B-rep geometry into triangle meshes.
package step

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"
)

// Kind identifies the type of a parameter value.
type Kind uint8

const (
	KindNull    Kind = iota // $
	KindDerived             // *
	KindNumber
	KindString
	KindEnum
	KindRef
	KindList
	KindTyped // TYPE_NAME(args), e.g. LENGTH_MEASURE(1.0)
)

// Value is a single entity parameter.
type Value struct {
	Kind Kind
	Num  float64
	Ref  int
	Str  string  // string contents, enum name (without dots) or typed parameter name
	List []Value // list elements or typed parameter arguments
}

// Part is one component of a complex entity instance.
type Part struct {
	Type string
	Args []Value
}

// Entity is a parsed entity instance from the DATA section.
type Entity struct {
	ID    int
	Type  string // empty for complex entities
	Args  []Value
	Parts []Part // set for complex entities: #1=(A(...)B(...));
}

// Is reports whether the entity is of the given type, or is a complex
// entity containing that type.
func (e *Entity) Is(t string) bool {
	if e == nil {
		return false
	}
	if e.Type == t {
		return true
	}
	for i := range e.Parts {
		if e.Parts[i].Type == t {
			return true
		}
	}
	return false
}

// PartArgs returns the arguments for the named type. For simple entities
// that is all the arguments (including inherited ones); for complex entities
// it is only the attributes declared by that part.
func (e *Entity) PartArgs(t string) ([]Value, bool) {
	if e == nil {
		return nil, false
	}
	if e.Type == t {
		return e.Args, true
	}
	for i := range e.Parts {
		if e.Parts[i].Type == t {
			return e.Parts[i].Args, true
		}
	}
	return nil, false
}

// Arg returns the i'th argument of a simple entity, or a null value.
func (e *Entity) Arg(i int) Value {
	if e == nil || i < 0 || i >= len(e.Args) {
		return Value{}
	}
	return e.Args[i]
}

// File is a parsed STEP file.
type File struct {
	Name   string // FILE_NAME name from the header
	dense  []*Entity
	sparse map[int]*Entity
	byType map[string][]*Entity
	count  int
}

// Get returns the entity with the given id, or nil.
func (f *File) Get(id int) *Entity {
	if f.sparse != nil {
		return f.sparse[id]
	}
	if id < 0 || id >= len(f.dense) {
		return nil
	}
	return f.dense[id]
}

// Ref resolves a reference value, or returns nil.
func (f *File) Ref(v Value) *Entity {
	if v.Kind != KindRef {
		return nil
	}
	return f.Get(v.Ref)
}

// OfType returns all entities of the given type, including complex entities
// containing that type as a part.
func (f *File) OfType(t string) []*Entity {
	return f.byType[t]
}

// Count returns the number of entities.
func (f *File) Count() int { return f.count }

// AsFloat returns the numeric value of v, unwrapping typed parameters such as
// LENGTH_MEASURE(1.0).
func (v Value) AsFloat() float64 {
	switch v.Kind {
	case KindNumber:
		return v.Num
	case KindTyped:
		if len(v.List) > 0 {
			return v.List[0].AsFloat()
		}
	}
	return 0
}

// AsBool interprets an enumeration as a STEP boolean/logical.
func (v Value) AsBool() bool {
	return v.Kind == KindEnum && (v.Str == "T" || v.Str == "TRUE")
}

// AsList returns list elements, or nil.
func (v Value) AsList() []Value {
	if v.Kind == KindList {
		return v.List
	}
	return nil
}

type record struct {
	start, end int
}

// Parse parses the contents of a STEP file.
func Parse(data []byte) (*File, error) {
	f := &File{byType: map[string][]*Entity{}}

	// Split into statements, separating the header from data records.
	var records []record
	section := ""
	i := 0
	n := len(data)
	for i < n {
		// Skip whitespace and comments between statements.
		i = skipSpace(data, i)
		if i >= n {
			break
		}
		start := i
		end, err := statementEnd(data, i)
		if err != nil {
			return nil, err
		}
		stmt := data[start:end]
		i = end + 1
		trimmed := bytes.TrimSpace(stmt)
		switch {
		case bytes.Equal(trimmed, []byte("HEADER")):
			section = "HEADER"
			continue
		case bytes.Equal(trimmed, []byte("DATA")) || bytes.HasPrefix(trimmed, []byte("DATA(")) || bytes.HasPrefix(trimmed, []byte("DATA (")):
			section = "DATA"
			continue
		case bytes.Equal(trimmed, []byte("ENDSEC")):
			section = ""
			continue
		}
		switch section {
		case "DATA":
			if len(trimmed) > 0 && trimmed[0] == '#' {
				records = append(records, record{start, end})
			}
		case "HEADER":
			if bytes.HasPrefix(trimmed, []byte("FILE_NAME")) {
				p := parser{s: stmt, intern: map[string]string{}}
				p.i = bytes.IndexByte(stmt, '(')
				if p.i >= 0 {
					if args, err := p.parseList(); err == nil && len(args) > 0 && args[0].Kind == KindString {
						f.Name = args[0].Str
					}
				}
			}
		}
	}
	if len(records) == 0 {
		return nil, errors.New("step: no DATA records found (is this a STEP file?)")
	}

	// Parse records in parallel.
	ents := make([]*Entity, len(records))
	workers := runtime.GOMAXPROCS(0)
	chunk := (len(records) + workers - 1) / workers
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for w := 0; w < workers; w++ {
		lo := w * chunk
		hi := min(lo+chunk, len(records))
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			p := parser{intern: map[string]string{}}
			for k := lo; k < hi; k++ {
				r := records[k]
				p.s = data[r.start:r.end]
				p.i = 0
				e, err := p.parseRecord()
				if err != nil {
					// Skip malformed records but remember the first error.
					if errs[w] == nil {
						errs[w] = fmt.Errorf("step: record at byte %d: %w", r.start, err)
					}
					continue
				}
				ents[k] = e
			}
		}(w, lo, hi)
	}
	wg.Wait()

	maxID := 0
	count := 0
	for _, e := range ents {
		if e == nil {
			continue
		}
		count++
		maxID = max(maxID, e.ID)
	}
	if count == 0 {
		for _, err := range errs {
			if err != nil {
				return nil, err
			}
		}
		return nil, errors.New("step: no entities parsed")
	}
	f.count = count
	if maxID <= 4*count+1024 {
		f.dense = make([]*Entity, maxID+1)
		for _, e := range ents {
			if e != nil {
				f.dense[e.ID] = e
			}
		}
	} else {
		f.sparse = make(map[int]*Entity, count)
		for _, e := range ents {
			if e != nil {
				f.sparse[e.ID] = e
			}
		}
	}
	for _, e := range ents {
		if e == nil {
			continue
		}
		if e.Type != "" {
			f.byType[e.Type] = append(f.byType[e.Type], e)
		}
		for _, p := range e.Parts {
			f.byType[p.Type] = append(f.byType[p.Type], e)
		}
	}
	return f, nil
}

func skipSpace(s []byte, i int) int {
	for i < len(s) {
		c := s[i]
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
			i++
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '*' {
			j := bytes.Index(s[i+2:], []byte("*/"))
			if j < 0 {
				return len(s)
			}
			i += j + 4
			continue
		}
		break
	}
	return i
}

// statementEnd returns the index of the ';' terminating the statement that
// starts at i.
func statementEnd(s []byte, i int) (int, error) {
	for i < len(s) {
		switch s[i] {
		case '\'':
			i++
			for i < len(s) {
				if s[i] == '\'' {
					if i+1 < len(s) && s[i+1] == '\'' {
						i += 2
						continue
					}
					break
				}
				i++
			}
			i++
		case '"':
			j := bytes.IndexByte(s[i+1:], '"')
			if j < 0 {
				return 0, errors.New("step: unterminated binary literal")
			}
			i += j + 2
		case '/':
			if i+1 < len(s) && s[i+1] == '*' {
				j := bytes.Index(s[i+2:], []byte("*/"))
				if j < 0 {
					return 0, errors.New("step: unterminated comment")
				}
				i += j + 4
			} else {
				i++
			}
		case ';':
			return i, nil
		default:
			i++
		}
	}
	return len(s), nil
}

type parser struct {
	s      []byte
	i      int
	intern map[string]string
}

func (p *parser) ws() {
	p.i = skipSpace(p.s, p.i)
}

func (p *parser) errorf(format string, args ...any) error {
	ctx := p.s[p.i:min(len(p.s), p.i+20)]
	return fmt.Errorf(format+" near %q", append(args, ctx)...)
}

func (p *parser) parseRecord() (*Entity, error) {
	p.ws()
	if p.i >= len(p.s) || p.s[p.i] != '#' {
		return nil, p.errorf("expected '#'")
	}
	p.i++
	id, ok := p.parseInt()
	if !ok {
		return nil, p.errorf("bad entity id")
	}
	p.ws()
	if p.i >= len(p.s) || p.s[p.i] != '=' {
		return nil, p.errorf("expected '='")
	}
	p.i++
	p.ws()
	e := &Entity{ID: id}
	if p.i < len(p.s) && p.s[p.i] == '(' {
		// Complex entity.
		p.i++
		for {
			p.ws()
			if p.i >= len(p.s) {
				return nil, p.errorf("unterminated complex entity")
			}
			if p.s[p.i] == ')' {
				p.i++
				break
			}
			name := p.parseKeyword()
			if name == "" {
				return nil, p.errorf("expected keyword in complex entity")
			}
			p.ws()
			args, err := p.parseList()
			if err != nil {
				return nil, err
			}
			e.Parts = append(e.Parts, Part{Type: name, Args: args})
		}
		return e, nil
	}
	name := p.parseKeyword()
	if name == "" {
		return nil, p.errorf("expected entity keyword")
	}
	p.ws()
	args, err := p.parseList()
	if err != nil {
		return nil, err
	}
	e.Type = name
	e.Args = args
	return e, nil
}

func (p *parser) parseInt() (int, bool) {
	start := p.i
	v := 0
	for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
		v = v*10 + int(p.s[p.i]-'0')
		p.i++
	}
	return v, p.i > start
}

func isKeywordChar(c byte) bool {
	return c == '_' || c == '-' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

func (p *parser) parseKeyword() string {
	start := p.i
	if p.i < len(p.s) && p.s[p.i] == '!' { // user-defined keyword
		p.i++
	}
	for p.i < len(p.s) && isKeywordChar(p.s[p.i]) {
		p.i++
	}
	if p.i == start {
		return ""
	}
	b := p.s[start:p.i]
	if s, ok := p.intern[string(b)]; ok {
		return s
	}
	s := strings.ToUpper(string(b))
	p.intern[string(b)] = s
	return s
}

// parseList parses "(v, v, ...)" with p.i at the '('.
func (p *parser) parseList() ([]Value, error) {
	if p.i >= len(p.s) || p.s[p.i] != '(' {
		return nil, p.errorf("expected '('")
	}
	p.i++
	var out []Value
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == ')' {
		p.i++
		return []Value{}, nil
	}
	for {
		p.ws()
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.ws()
		if p.i >= len(p.s) {
			return nil, p.errorf("unterminated list")
		}
		switch p.s[p.i] {
		case ',':
			p.i++
		case ')':
			p.i++
			return out, nil
		default:
			return nil, p.errorf("expected ',' or ')'")
		}
	}
}

func (p *parser) parseValue() (Value, error) {
	if p.i >= len(p.s) {
		return Value{}, p.errorf("unexpected end")
	}
	c := p.s[p.i]
	switch {
	case c == '$':
		p.i++
		return Value{Kind: KindNull}, nil
	case c == '*':
		p.i++
		return Value{Kind: KindDerived}, nil
	case c == '#':
		p.i++
		id, ok := p.parseInt()
		if !ok {
			return Value{}, p.errorf("bad reference")
		}
		return Value{Kind: KindRef, Ref: id}, nil
	case c == '\'':
		s, err := p.parseString()
		if err != nil {
			return Value{}, err
		}
		return Value{Kind: KindString, Str: s}, nil
	case c == '"':
		j := bytes.IndexByte(p.s[p.i+1:], '"')
		if j < 0 {
			return Value{}, p.errorf("unterminated binary")
		}
		s := string(p.s[p.i+1 : p.i+1+j])
		p.i += j + 2
		return Value{Kind: KindString, Str: s}, nil
	case c == '(':
		l, err := p.parseList()
		if err != nil {
			return Value{}, err
		}
		return Value{Kind: KindList, List: l}, nil
	case c == '.' && p.i+1 < len(p.s) && (p.s[p.i+1] < '0' || p.s[p.i+1] > '9'):
		j := bytes.IndexByte(p.s[p.i+1:], '.')
		if j < 0 {
			return Value{}, p.errorf("unterminated enum")
		}
		name := p.s[p.i+1 : p.i+1+j]
		p.i += j + 2
		s, ok := p.intern[string(name)]
		if !ok {
			s = strings.ToUpper(string(name))
			p.intern[string(name)] = s
		}
		return Value{Kind: KindEnum, Str: s}, nil
	case c == '-' || c == '+' || c == '.' || (c >= '0' && c <= '9'):
		start := p.i
		p.i++
		for p.i < len(p.s) {
			d := p.s[p.i]
			if (d >= '0' && d <= '9') || d == '.' || d == 'E' || d == 'e' || d == '-' || d == '+' {
				p.i++
				continue
			}
			break
		}
		num, err := parseNumber(p.s[start:p.i])
		if err != nil {
			return Value{}, p.errorf("bad number")
		}
		return Value{Kind: KindNumber, Num: num}, nil
	case isKeywordChar(c) || c == '!':
		name := p.parseKeyword()
		p.ws()
		if p.i < len(p.s) && p.s[p.i] == '(' {
			args, err := p.parseList()
			if err != nil {
				return Value{}, err
			}
			return Value{Kind: KindTyped, Str: name, List: args}, nil
		}
		return Value{Kind: KindEnum, Str: name}, nil
	}
	return Value{}, p.errorf("unexpected character %q", c)
}

func parseNumber(b []byte) (float64, error) {
	// STEP allows reals like "1." and "1.E-3" which strconv accepts.
	// Fast path for plain integers.
	neg := false
	i := 0
	if len(b) > 0 && (b[0] == '-' || b[0] == '+') {
		neg = b[0] == '-'
		i++
	}
	if i < len(b) && len(b)-i <= 15 {
		v := int64(0)
		j := i
		for ; j < len(b) && b[j] >= '0' && b[j] <= '9'; j++ {
			v = v*10 + int64(b[j]-'0')
		}
		if j == len(b) && j > i {
			if neg {
				v = -v
			}
			return float64(v), nil
		}
	}
	return strconv.ParseFloat(string(b), 64)
}

func (p *parser) parseString() (string, error) {
	p.i++ // opening quote
	start := p.i
	simple := true
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == '\'' {
			if p.i+1 < len(p.s) && p.s[p.i+1] == '\'' {
				simple = false
				p.i += 2
				continue
			}
			break
		}
		if c == '\\' || c == '\n' || c == '\r' {
			simple = false
		}
		p.i++
	}
	if p.i >= len(p.s) {
		return "", p.errorf("unterminated string")
	}
	raw := p.s[start:p.i]
	p.i++ // closing quote
	if simple {
		if len(raw) == 0 {
			return "", nil
		}
		return string(raw), nil
	}
	return decodeString(raw), nil
}

// decodeString handles ” escapes and the \X\, \X2\, \X4\, \S\ and \P\
// encodings of ISO 10303-21.
func decodeString(raw []byte) string {
	var sb strings.Builder
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '\n' || c == '\r':
			// Line breaks inside strings are not significant.
		case c == '\'' && i+1 < len(raw) && raw[i+1] == '\'':
			sb.WriteByte('\'')
			i++
		case c == '\\' && i+1 < len(raw):
			rest := raw[i:]
			switch {
			case bytes.HasPrefix(rest, []byte(`\\`)):
				sb.WriteByte('\\')
				i++
			case bytes.HasPrefix(rest, []byte(`\X2\`)):
				j := i + 4
				var units []uint16
				for j+4 <= len(raw) && raw[j] != '\\' {
					v, err := strconv.ParseUint(string(raw[j:j+4]), 16, 16)
					if err != nil {
						break
					}
					units = append(units, uint16(v))
					j += 4
				}
				sb.WriteString(string(utf16.Decode(units)))
				if bytes.HasPrefix(raw[j:], []byte(`\X0\`)) {
					j += 4
				}
				i = j - 1
			case bytes.HasPrefix(rest, []byte(`\X4\`)):
				j := i + 4
				for j+8 <= len(raw) && raw[j] != '\\' {
					v, err := strconv.ParseUint(string(raw[j:j+8]), 16, 32)
					if err != nil {
						break
					}
					sb.WriteRune(rune(v))
					j += 8
				}
				if bytes.HasPrefix(raw[j:], []byte(`\X0\`)) {
					j += 4
				}
				i = j - 1
			case bytes.HasPrefix(rest, []byte(`\X\`)) && i+5 <= len(raw):
				v, err := strconv.ParseUint(string(raw[i+3:i+5]), 16, 8)
				if err == nil {
					sb.WriteRune(rune(v))
				}
				i += 4
			case bytes.HasPrefix(rest, []byte(`\S\`)) && i+3 < len(raw):
				sb.WriteRune(rune(raw[i+3]) + 128)
				i += 3
			case len(rest) >= 4 && rest[1] == 'P' && rest[3] == '\\':
				i += 3 // code page directive, ignored
			default:
				sb.WriteByte(c)
			}
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}
