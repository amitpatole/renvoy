package pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/amitpatole/renvoy/internal/diagnostics"
)

// MaxNodes bounds how many YAML nodes the strict decoder visits, including nodes
// reached through aliases, so alias-expansion documents cannot exhaust memory or CPU.
const MaxNodes = 100_000

// Position is a 1-based source location.
type Position struct {
	Line   int
	Column int
}

// SourceMap records where each config path appears in the source file.
type SourceMap struct {
	File      string
	positions map[string]Position
}

// NewSourceMap returns an empty source map for a file.
func NewSourceMap(file string) *SourceMap {
	return &SourceMap{File: file, positions: map[string]Position{}}
}

func (m *SourceMap) record(path string, n *yaml.Node) {
	if _, ok := m.positions[path]; !ok && n != nil && n.Line > 0 {
		m.positions[path] = Position{Line: n.Line, Column: n.Column}
	}
}

// Lookup finds the position of path, falling back to its nearest recorded ancestor.
func (m *SourceMap) Lookup(path string) (Position, bool) {
	if m == nil {
		return Position{}, false
	}
	for {
		if p, ok := m.positions[path]; ok {
			return p, true
		}
		parent, ok := ParentPath(path)
		if !ok {
			return Position{}, false
		}
		path = parent
	}
}

// Locate fills file and position information on diagnostics that lack it.
func (m *SourceMap) Locate(l diagnostics.List) diagnostics.List {
	if m == nil {
		return l
	}
	out := make(diagnostics.List, len(l))
	for i, d := range l {
		if d.File == "" {
			d.File = m.File
		}
		if d.Line == 0 && d.Path != "" {
			if p, ok := m.Lookup(d.Path); ok {
				d.Line, d.Column = p.Line, p.Column
			}
		}
		out[i] = d
	}
	return out
}

// ParentPath strips the last segment from a config path.
func ParentPath(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	if strings.HasSuffix(path, "]") {
		if i := strings.LastIndex(path, "["); i >= 0 {
			return path[:i], true
		}
	}
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[:i], true
	}
	return "", true
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// JoinKey appends a mapping key to a config path.
func JoinKey(path, key string) string {
	if !identRe.MatchString(key) {
		return fmt.Sprintf("%s[%s]", path, strconv.Quote(key))
	}
	if path == "" {
		return key
	}
	return path + "." + key
}

// JoinIndex appends a sequence index to a config path.
func JoinIndex(path string, i int) string { return fmt.Sprintf("%s[%d]", path, i) }

// Parse decodes a renvoy.yaml document strictly. It returns a nil pipeline when
// the document is structurally invalid.
func Parse(data []byte, file string) (*Pipeline, *SourceMap, diagnostics.List) {
	sm := NewSourceMap(file)
	root, diags := parseRoot(data)
	if root == nil {
		return nil, sm, sm.Locate(diags)
	}
	var p Pipeline
	diags = append(diags, DecodeStrict(root, &p, "", sm)...)
	if diags.HasErrors() {
		return nil, sm, sm.Locate(diags)
	}
	return &p, sm, sm.Locate(diags)
}

func parseRoot(data []byte) (*yaml.Node, diagnostics.List) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, diagnostics.List{diagnostics.Errorf("", "the file is empty").
				WithHint("run `renvoy init` to create a starter pipeline")}
		}
		return nil, diagnostics.List{yamlErrorDiagnostic(err)}
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, diagnostics.List{{Severity: diagnostics.Error, Line: extra.Line, Column: extra.Column,
			Message: "the file contains more than one YAML document; Renvoy reads exactly one"}}
	} else if !errors.Is(err, io.EOF) {
		return nil, diagnostics.List{yamlErrorDiagnostic(err)}
	}
	if len(doc.Content) == 0 {
		return nil, diagnostics.List{diagnostics.Errorf("", "the file is empty")}
	}
	return doc.Content[0], nil
}

var yamlLineRe = regexp.MustCompile(`^(?:yaml: )?line (\d+): (.*)$`)

func yamlErrorDiagnostic(err error) diagnostics.Diagnostic {
	msg := err.Error()
	d := diagnostics.Diagnostic{Severity: diagnostics.Error}
	if m := yamlLineRe.FindStringSubmatch(msg); m != nil {
		d.Line, _ = strconv.Atoi(m[1])
		msg = m[2]
	}
	d.Message = "invalid YAML: " + strings.TrimPrefix(msg, "yaml: ")
	return d
}

// DecodeStrict checks node against the shape of out (a pointer), reporting unknown
// fields and shape mismatches with paths, then decodes it when no errors were found.
// Providers use it to decode their extension blocks.
func DecodeStrict(n *yaml.Node, out any, path string, sm *SourceMap) diagnostics.List {
	w := &walker{sm: sm}
	if sm == nil {
		w.sm = NewSourceMap("")
	}
	w.walk(n, reflect.TypeOf(out).Elem(), path)
	if w.diags.HasErrors() {
		return w.diags
	}
	if err := n.Decode(out); err != nil {
		var te *yaml.TypeError
		if errors.As(err, &te) {
			for _, e := range te.Errors {
				d := yamlErrorDiagnostic(errors.New(e))
				d.Message = strings.TrimPrefix(d.Message, "invalid YAML: ")
				w.diags = append(w.diags, d)
			}
		} else {
			d := diagnostics.Errorf(path, "%v", err)
			w.diags = append(w.diags, d)
		}
	}
	return w.diags
}

type walker struct {
	sm      *SourceMap
	diags   diagnostics.List
	visited int
	limited bool
}

var (
	varsType       = reflect.TypeOf(Vars{})
	matrixType     = reflect.TypeOf(Matrix{})
	extensionsType = reflect.TypeOf(Extensions{})
	envType        = reflect.TypeOf(Environment{})
)

func (w *walker) errorf(path string, n *yaml.Node, format string, args ...any) {
	d := diagnostics.Errorf(path, format, args...)
	if n != nil {
		d.Line, d.Column = n.Line, n.Column
	}
	w.diags = append(w.diags, d)
}

func (w *walker) walk(n *yaml.Node, t reflect.Type, path string) {
	if w.limited {
		return
	}
	w.visited++
	if w.visited > MaxNodes {
		w.limited = true
		w.errorf(path, n, "the document is too large or uses excessive aliasing (more than %d nodes)", MaxNodes)
		return
	}
	n = resolveAlias(n)
	w.sm.record(path, n)

	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t {
	case varsType:
		w.walkScalarMap(n, path)
		return
	case matrixType:
		w.walkMatrix(n, path)
		return
	case extensionsType:
		w.expectKind(n, path, yaml.MappingNode, "a mapping")
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				w.sm.record(JoinKey(path, n.Content[i].Value), n.Content[i])
			}
		}
		return
	case envType:
		if n.Kind == yaml.ScalarNode && !isNull(n) {
			return
		}
	}

	switch t.Kind() {
	case reflect.Struct:
		w.walkStruct(n, t, path)
	case reflect.Slice:
		if !w.expectKind(n, path, yaml.SequenceNode, "a list") {
			return
		}
		for i, item := range n.Content {
			w.walk(item, t.Elem(), JoinIndex(path, i))
		}
	case reflect.Map:
		if !w.expectKind(n, path, yaml.MappingNode, "a mapping") {
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			kp := JoinKey(path, k.Value)
			w.sm.record(kp, k)
			w.walk(n.Content[i+1], t.Elem(), kp)
		}
	case reflect.String, reflect.Bool, reflect.Int:
		if n.Kind != yaml.ScalarNode {
			w.errorf(path, n, "expected a %s, found %s", scalarName(t.Kind()), kindName(n))
		}
	}
}

func (w *walker) walkStruct(n *yaml.Node, t reflect.Type, path string) {
	if !w.expectKind(n, path, yaml.MappingNode, "a mapping") {
		return
	}
	fields := yamlFields(t)
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Value == "<<" {
			w.walk(v, t, path)
			continue
		}
		kp := JoinKey(path, k.Value)
		w.sm.record(kp, k)
		f, ok := fields[k.Value]
		if !ok {
			d := diagnostics.Errorf(kp, "unknown field %q", k.Value)
			d.Line, d.Column = k.Line, k.Column
			if s := suggest(k.Value, fields); s != "" {
				d = d.WithHint("did you mean %q?", s)
			}
			w.diags = append(w.diags, d)
			continue
		}
		w.walk(v, f, kp)
	}
}

func (w *walker) walkScalarMap(n *yaml.Node, path string) {
	if !w.expectKind(n, path, yaml.MappingNode, "a mapping") {
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], resolveAlias(n.Content[i+1])
		kp := JoinKey(path, k.Value)
		w.sm.record(kp, k)
		w.visited++
		switch {
		case v.Kind != yaml.ScalarNode:
			w.errorf(kp, v, "expected a string value, found %s", kindName(v))
		case isNull(v):
			w.errorf(kp, v, "%q has no value", k.Value)
			w.diags[len(w.diags)-1].Hint = `use "" for an empty string`
		}
	}
}

func (w *walker) walkMatrix(n *yaml.Node, path string) {
	if !w.expectKind(n, path, yaml.MappingNode, "a mapping of axis names to value lists") {
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], resolveAlias(n.Content[i+1])
		kp := JoinKey(path, k.Value)
		w.sm.record(kp, k)
		if !w.expectKind(v, kp, yaml.SequenceNode, "a list of values") {
			continue
		}
		for j, item := range v.Content {
			item = resolveAlias(item)
			w.visited++
			if item.Kind != yaml.ScalarNode || isNull(item) {
				w.errorf(JoinIndex(kp, j), item, "matrix values must be strings or numbers, found %s", kindName(item))
			}
		}
	}
}

func (w *walker) expectKind(n *yaml.Node, path string, want yaml.Kind, desc string) bool {
	if n.Kind == want {
		return true
	}
	if isNull(n) {
		w.errorf(path, n, "expected %s, found an empty value", desc)
		switch want {
		case yaml.MappingNode:
			w.diags[len(w.diags)-1].Hint = "use {} for an empty mapping"
		case yaml.SequenceNode:
			w.diags[len(w.diags)-1].Hint = "use [] for an empty list"
		}
		return false
	}
	w.errorf(path, n, "expected %s, found %s", desc, kindName(n))
	return false
}

func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

func kindName(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "a mapping"
	case yaml.SequenceNode:
		return "a list"
	case yaml.ScalarNode:
		if isNull(n) {
			return "an empty value"
		}
		return fmt.Sprintf("the scalar %q", n.Value)
	default:
		return "an unsupported node"
	}
}

func scalarName(k reflect.Kind) string {
	switch k {
	case reflect.Bool:
		return "boolean"
	case reflect.Int:
		return "integer"
	default:
		return "string"
	}
}

func yamlFields(t reflect.Type) map[string]reflect.Type {
	fields := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		fields[tag] = f.Type
	}
	return fields
}

// YAMLFieldNames returns the yaml tag names of a struct type, for schema drift checks.
func YAMLFieldNames(t reflect.Type) []string {
	var names []string
	for i := 0; i < t.NumField(); i++ {
		tag := strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0]
		if tag != "" && tag != "-" {
			names = append(names, tag)
		}
	}
	return names
}

func suggest(got string, fields map[string]reflect.Type) string {
	best, bestDist := "", 3
	for name := range fields {
		if strings.EqualFold(name, got) {
			return name
		}
		if d := levenshtein(got, name); d < bestDist || (d == bestDist && best != "" && name < best) {
			best, bestDist = name, d
		}
	}
	return best
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
