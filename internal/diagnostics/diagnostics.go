// Package diagnostics defines structured errors, warnings, and informational
// messages produced while loading, validating, and rendering pipelines.
package diagnostics

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Severity ranks a diagnostic.
type Severity int

const (
	// Info reports a fact the user should know but need not act on.
	Info Severity = iota
	// Warning reports a semantic difference or likely mistake that does not block generation.
	Warning
	// Error blocks validation or generation.
	Error
)

func (s Severity) String() string {
	switch s {
	case Error:
		return "error"
	case Warning:
		return "warning"
	default:
		return "info"
	}
}

// MarshalJSON encodes the severity as its lowercase name.
func (s Severity) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// UnmarshalJSON decodes a lowercase severity name.
func (s *Severity) UnmarshalJSON(b []byte) error {
	var name string
	if err := json.Unmarshal(b, &name); err != nil {
		return err
	}
	switch name {
	case "error":
		*s = Error
	case "warning":
		*s = Warning
	case "info":
		*s = Info
	default:
		return fmt.Errorf("unknown severity %q", name)
	}
	return nil
}

// Diagnostic is a single structured finding.
type Diagnostic struct {
	Severity Severity `json:"severity"`
	Provider string   `json:"provider,omitempty"`
	File     string   `json:"file,omitempty"`
	Path     string   `json:"path,omitempty"`
	Line     int      `json:"line,omitempty"`
	Column   int      `json:"column,omitempty"`
	Message  string   `json:"message"`
	Hint     string   `json:"hint,omitempty"`
}

// Errorf builds an error diagnostic at a config path.
func Errorf(path, format string, args ...any) Diagnostic {
	return Diagnostic{Severity: Error, Path: path, Message: fmt.Sprintf(format, args...)}
}

// Warningf builds a warning diagnostic at a config path.
func Warningf(path, format string, args ...any) Diagnostic {
	return Diagnostic{Severity: Warning, Path: path, Message: fmt.Sprintf(format, args...)}
}

// Infof builds an informational diagnostic at a config path.
func Infof(path, format string, args ...any) Diagnostic {
	return Diagnostic{Severity: Info, Path: path, Message: fmt.Sprintf(format, args...)}
}

// WithHint returns a copy of d carrying a remediation hint.
func (d Diagnostic) WithHint(format string, args ...any) Diagnostic {
	d.Hint = fmt.Sprintf(format, args...)
	return d
}

// WithProvider returns a copy of d attributed to a provider.
func (d Diagnostic) WithProvider(name string) Diagnostic {
	d.Provider = name
	return d
}

// Location returns "file:line:col path" style location text, omitting unknown parts.
func (d Diagnostic) Location() string {
	var parts []string
	if d.File != "" {
		loc := d.File
		if d.Line > 0 {
			loc += fmt.Sprintf(":%d", d.Line)
			if d.Column > 0 {
				loc += fmt.Sprintf(":%d", d.Column)
			}
		}
		parts = append(parts, loc)
	}
	if d.Path != "" {
		parts = append(parts, d.Path)
	}
	return strings.Join(parts, " ")
}

// String renders the diagnostic in the human-readable text format.
func (d Diagnostic) String() string {
	var b strings.Builder
	b.WriteString(d.Severity.String())
	if d.Provider != "" {
		fmt.Fprintf(&b, " [%s]", d.Provider)
	}
	if loc := d.Location(); loc != "" {
		b.WriteString(" ")
		b.WriteString(loc)
	}
	b.WriteString(":\n  ")
	b.WriteString(strings.ReplaceAll(d.Message, "\n", "\n  "))
	if d.Hint != "" {
		b.WriteString("\n  hint: ")
		b.WriteString(strings.ReplaceAll(d.Hint, "\n", "\n  "))
	}
	return b.String()
}

// List is an ordered collection of diagnostics.
type List []Diagnostic

// HasErrors reports whether any diagnostic is an error.
func (l List) HasErrors() bool {
	for _, d := range l {
		if d.Severity == Error {
			return true
		}
	}
	return false
}

// Count returns the number of diagnostics with the given severity.
func (l List) Count(s Severity) int {
	n := 0
	for _, d := range l {
		if d.Severity == s {
			n++
		}
	}
	return n
}

// Sorted returns a copy ordered by file position, then severity (errors first), then text,
// so output is deterministic regardless of the order validators ran in.
func (l List) Sorted() List {
	out := make(List, len(l))
	copy(out, l)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		if a.Severity != b.Severity {
			return a.Severity > b.Severity
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Message < b.Message
	})
	return out
}

// WriteText writes diagnostics in the text format, one blank line apart.
// Control and bidirectional-override characters from the input are escaped so
// they cannot manipulate the terminal.
func WriteText(w io.Writer, l List) error {
	for i, d := range l {
		if i > 0 {
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(w, Sanitize(d.String())+"\n"); err != nil {
			return err
		}
	}
	return nil
}

// Sanitize escapes control characters (other than newline) and bidirectional
// formatting characters so text is safe to print to a terminal.
func Sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f),
			r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x200e || r == 0x200f || r == 0x061c:
			fmt.Fprintf(&b, "\\u%04x", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Report is the JSON document emitted by --format json.
type Report struct {
	Valid       bool         `json:"valid"`
	Errors      int          `json:"errors"`
	Warnings    int          `json:"warnings"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// WriteJSON writes diagnostics as an indented JSON report.
func WriteJSON(w io.Writer, l List) error {
	r := Report{
		Valid:       !l.HasErrors(),
		Errors:      l.Count(Error),
		Warnings:    l.Count(Warning),
		Diagnostics: l,
	}
	if r.Diagnostics == nil {
		r.Diagnostics = []Diagnostic{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
