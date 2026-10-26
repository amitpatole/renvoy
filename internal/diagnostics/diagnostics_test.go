package diagnostics_test

import (
	"strings"
	"testing"

	"github.com/amitpatole/renvoy/internal/diagnostics"
)

func TestWriteTextSanitizesControls(t *testing.T) {
	d := diagnostics.Diagnostic{Severity: diagnostics.Error, Message: "bad \x1b[31mred\x1b[0m \u202ereversed\u202c\x07",
		Hint: "multi\nline"}
	var b strings.Builder
	if err := diagnostics.WriteText(&b, diagnostics.List{d}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.ContainsAny(out, "\x1b\u202e\u202c\x07") {
		t.Fatalf("raw control characters in output: %q", out)
	}
	for _, want := range []string{`\u001b[31m`, `\u202e`, `\u0007`, "multi\n  line"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q: %q", want, out)
		}
	}
}

func TestSortedIsDeterministic(t *testing.T) {
	l := diagnostics.List{
		{Severity: diagnostics.Warning, Line: 2, Message: "b"},
		{Severity: diagnostics.Error, Line: 2, Message: "a"},
		{Severity: diagnostics.Info, Line: 1, Message: "c"},
	}
	got := l.Sorted()
	if got[0].Message != "c" || got[1].Message != "a" || got[2].Message != "b" {
		t.Fatalf("order: %+v", got)
	}
}
