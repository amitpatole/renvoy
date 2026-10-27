// Package shared holds rendering helpers used by several providers.
package shared

import (
	"bytes"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Map is an insertion-ordered YAML mapping builder.
type Map struct{ n *yaml.Node }

// NewMap returns an empty mapping.
func NewMap() *Map { return &Map{n: &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}} }

// Set appends key with a value. Supported values: string, int, bool, []string,
// *Map, *yaml.Node, and []*Map.
func (m *Map) Set(key string, v any) *Map {
	m.n.Content = append(m.n.Content, Str(key), Node(v))
	return m
}

// Len returns the number of keys.
func (m *Map) Len() int { return len(m.n.Content) / 2 }

// Node returns the underlying node.
func (m *Map) Node() *yaml.Node { return m.n }

// Node converts a supported Go value to a YAML node.
func Node(v any) *yaml.Node {
	switch x := v.(type) {
	case *yaml.Node:
		return x
	case *Map:
		return x.n
	case string:
		return Str(x)
	case int:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(x)}
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(x)}
	case []string:
		s := Seq()
		for _, item := range x {
			s.Content = append(s.Content, Str(item))
		}
		return s
	case []*Map:
		s := Seq()
		for _, item := range x {
			s.Content = append(s.Content, item.n)
		}
		return s
	default:
		panic(fmt.Sprintf("shared.Node: unsupported type %T", v))
	}
}

// Str returns a string scalar; multi-line strings use literal block style.
// Strings that a YAML 1.1 parser would not read as strings are double-quoted.
func Str(s string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
	switch {
	case strings.Contains(s, "\n"):
		n.Style = yaml.LiteralStyle
	case yaml11NonString(s):
		n.Style = yaml.DoubleQuotedStyle
	}
	return n
}

// The encoder quotes strings that YAML 1.2 would resolve to another type, but
// GitLab (Ruby Psych) and Bamboo (SnakeYAML) parse YAML 1.1, which also reads
// yes/no/on/off as booleans, 12:30 and 1,000 as integers, spaced timestamps as
// times, << and = as merge and value keys, and Psych reads :name as a symbol.
var yaml11Patterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(y|n|yes|no|on|off|true|false|null|~)$`),
	regexp.MustCompile(`^[-+]?(\.[0-9]|[0-9_])[0-9_,:.]*([eE][-+]?[0-9]+)?$`),
	regexp.MustCompile(`^[-+]?0[xXoObB][0-9a-fA-F_,]*$`),
	regexp.MustCompile(`^[-+]?\.(inf|Inf|INF|nan|NaN|NAN)$`),
	regexp.MustCompile(`^-?[0-9]{4}-[0-9]{1,2}-[0-9]{1,2}([Tt \t]|$)`),
	regexp.MustCompile(`^(<<|=|:.*)$`),
}

func yaml11NonString(s string) bool {
	for _, re := range yaml11Patterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// SetPlainKey is Set for a fixed key that the target reads as a string even
// though YAML 1.1 would not (GitHub Actions' "on").
func (m *Map) SetPlainKey(key string, v any) *Map {
	m.n.Content = append(m.n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, Node(v))
	return m
}

// Quoted returns a double-quoted string scalar.
func Quoted(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s, Style: yaml.DoubleQuotedStyle}
}

// Null returns an empty (null) scalar, rendered as a bare key.
func Null() *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: ""} }

// Seq returns a sequence of nodes.
func Seq(items ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: items}
}

// FlowSeq returns a flow-style sequence of strings, e.g. [main, develop].
func FlowSeq(items []string) *yaml.Node {
	n := Node(items)
	n.Style = yaml.FlowStyle
	return n
}

// Block normalizes a script for literal block output: one trailing newline when
// multi-line, none when single-line.
func Block(script string) string {
	s := strings.TrimRight(script, "\n")
	if strings.Contains(s, "\n") {
		return s + "\n"
	}
	return s
}

// Encode renders a mapping with two-space indentation and blank lines between
// top-level sections. Entries under the top-level keys named in spaced are also
// separated by blank lines.
func Encode(m *Map, spaced ...string) ([]byte, error) {
	var out bytes.Buffer
	for i := 0; i < len(m.n.Content); i += 2 {
		part := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: m.n.Content[i : i+2]}
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(part); err != nil {
			return nil, err
		}
		if err := enc.Close(); err != nil {
			return nil, err
		}
		if i > 0 && (part.Content[1].Kind == yaml.MappingNode || part.Content[1].Kind == yaml.SequenceNode ||
			m.n.Content[i-1].Kind == yaml.MappingNode || m.n.Content[i-1].Kind == yaml.SequenceNode) {
			out.WriteByte('\n')
		}
		if slices.Contains(spaced, part.Content[0].Value) {
			out.Write(spaceSafely(buf.Bytes()))
		} else {
			out.Write(buf.Bytes())
		}
	}
	return out.Bytes(), nil
}

// spaceSafely adds blank lines between second-level entries, keeping the
// original text if spacing would change the parsed document.
func spaceSafely(b []byte) []byte {
	spaced := spaceChildren(b)
	var before, after any
	if yaml.Unmarshal(b, &before) != nil || yaml.Unmarshal(spaced, &after) != nil || !reflect.DeepEqual(before, after) {
		return b
	}
	return spaced
}

// spaceChildren inserts a blank line before every second-level key after the first.
// Second-level keys are the only lines indented by exactly two spaces.
func spaceChildren(b []byte) []byte {
	lines := strings.SplitAfter(string(b), "\n")
	var out strings.Builder
	seen := false
	for _, line := range lines {
		if strings.HasPrefix(line, "  ") && len(line) > 2 && line[2] != ' ' && line[2] != '-' && line[2] != '#' {
			if seen {
				out.WriteString("\n")
			}
			seen = true
		}
		out.WriteString(line)
	}
	return []byte(out.String())
}
