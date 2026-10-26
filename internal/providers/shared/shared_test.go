package shared

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/amitpatole/renvoy/internal/pipeline"
)

func envName(r pipeline.Ref) string { return r.Name }

// PowerShell ends double-quoted strings at typographic quotes as well as at '"',
// so they must be escaped or a value could close the string and run commands.
func TestPowerShellWordEscapesTypographicQuotes(t *testing.T) {
	for _, q := range []string{"“", "”", "„", `"`, "`", "$"} {
		word := Word(pipeline.MustTemplate("a"+q+"; Remove-Item x"), pipeline.ShellPwsh, envName)
		if !strings.Contains(word, "`"+q) {
			t.Errorf("%q is not escaped in %s", q, word)
		}
	}
}

func TestBashWordQuoting(t *testing.T) {
	cases := map[string]string{
		"plain/path:tag":       "plain/path:tag",
		`has "quotes" $x`:      `"has \"quotes\" \$x"`,
		"back`tick\\":          "\"back\\`tick\\\\\"",
		"-starts-with-dash":    `"-starts-with-dash"`,
		"${variables.IMAGE}:1": `"${IMAGE}:1"`,
	}
	for in, want := range cases {
		if got := Word(pipeline.MustTemplate(in), pipeline.ShellBash, envName); got != want {
			t.Errorf("Word(%q) = %s, want %s", in, got, want)
		}
	}
}

// The spacing pass edits encoded text, so it must keep the original whenever the
// edit would change the parsed document.
func TestSpacingNeverChangesMeaning(t *testing.T) {
	meaningful := []byte("jobs:\n  a: \"line one\n\n  b: still the same scalar\"\n  c: 1\n")
	if got := spaceSafely(meaningful); string(got) != string(meaningful) {
		t.Fatalf("spacing changed a multi-line scalar:\n%s", got)
	}
	plain := []byte("jobs:\n  a: 1\n  b: 2\n")
	if got := string(spaceSafely(plain)); got != "jobs:\n  a: 1\n\n  b: 2\n" {
		t.Fatalf("spacing = %q", got)
	}
}

// Round 14: strings that a YAML 1.1 parser (GitLab's Psych, Bamboo's SnakeYAML)
// would read as booleans, numbers, times, merge keys, or symbols are quoted.
func TestYAML11LookalikesAreQuoted(t *testing.T) {
	for _, s := range []string{"yes", "No", "ON", "off", "y", "N", "12:30", "1:30", "1,000", "1:30.5", "190:20:30.15",
		"<<", "=", ":foo", "_", "2001-12-14 21:59:43.10 -5", "1.2.3", "-.Inf", "0x1F"} {
		if n := Str(s); n.Style != yaml.DoubleQuotedStyle {
			t.Errorf("%q is not double-quoted", s)
		}
	}
	for _, s := range []string{"main", "ubuntu-latest", "v1.2", "python3", "on_success", "yesterday", "a:b", "1a"} {
		if n := Str(s); n.Style != 0 {
			t.Errorf("%q is quoted unnecessarily", s)
		}
	}
	out, err := Encode(NewMap().SetPlainKey("on", "push").Set("on", "yes"))
	if err != nil || !strings.Contains(string(out), "on: push\n") || !strings.Contains(string(out), `"on": "yes"`) {
		t.Fatalf("%s %v", out, err)
	}
}
