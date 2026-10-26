package pipeline

import (
	"fmt"
	"regexp"
	"strings"
)

// Reference namespaces.
const (
	NSVariables = "variables"
	NSSecrets   = "secrets"
	NSGit       = "git"
	NSMatrix    = "matrix"
)

// Git reference names.
const (
	GitSHA    = "sha"
	GitBranch = "branch"
)

// Ref is a ${namespace.name} reference.
type Ref struct {
	Namespace string
	Name      string
}

func (r Ref) String() string { return "${" + r.Namespace + "." + r.Name + "}" }

// Segment is either literal text or a reference.
type Segment struct {
	Literal string
	Ref     *Ref
}

// Template is a parsed string with embedded references.
type Template []Segment

// Refs returns the references in order of appearance.
func (t Template) Refs() []Ref {
	var refs []Ref
	for _, s := range t {
		if s.Ref != nil {
			refs = append(refs, *s.Ref)
		}
	}
	return refs
}

// IsLiteral reports whether the template contains no references.
func (t Template) IsLiteral() bool { return len(t.Refs()) == 0 }

// Literal returns the literal text of a reference-free template.
func (t Template) Literal() string {
	var b strings.Builder
	for _, s := range t {
		b.WriteString(s.Literal)
	}
	return b.String()
}

var (
	refBodyRe  = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\.(.*)$`)
	refNameRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	namespaces = map[string]bool{NSVariables: true, NSSecrets: true, NSGit: true, NSMatrix: true}
	gitNames   = map[string]bool{GitSHA: true, GitBranch: true}
)

// ParseTemplate splits s into literal text and references.
//
// A reference is ${namespace.name}. "$${" produces a literal "${". Any other "$"
// text, including shell forms such as $HOME and ${HOME}, is literal.
func ParseTemplate(s string) (Template, error) {
	var t Template
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			t = append(t, Segment{Literal: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "$${") {
			lit.WriteString("${")
			i += 3
			continue
		}
		if !strings.HasPrefix(s[i:], "${") {
			lit.WriteByte(s[i])
			i++
			continue
		}
		end := strings.IndexByte(s[i+2:], '}')
		if end < 0 {
			if refBodyRe.MatchString(s[i+2:]) {
				return nil, fmt.Errorf("unterminated reference %q", s[i:])
			}
			lit.WriteString(s[i:])
			break
		}
		body := s[i+2 : i+2+end]
		m := refBodyRe.FindStringSubmatch(body)
		if m == nil {
			lit.WriteString(s[i : i+3+end])
			i += 3 + end
			continue
		}
		ns, name := m[1], m[2]
		if !namespaces[ns] {
			return nil, fmt.Errorf("unknown reference namespace %q in ${%s}; expected one of variables, secrets, git, matrix", ns, body)
		}
		if !refNameRe.MatchString(name) {
			return nil, fmt.Errorf("invalid reference name %q in ${%s}", name, body)
		}
		if ns == NSGit && !gitNames[name] {
			return nil, fmt.Errorf("unknown git reference ${git.%s}; expected git.sha or git.branch", name)
		}
		flush()
		t = append(t, Segment{Ref: &Ref{Namespace: ns, Name: name}})
		i += 3 + end
	}
	flush()
	return t, nil
}

// MustTemplate parses a template that has already passed validation.
func MustTemplate(s string) Template {
	t, err := ParseTemplate(s)
	if err != nil {
		panic(fmt.Sprintf("renvoy: unvalidated template %q: %v", s, err))
	}
	return t
}
