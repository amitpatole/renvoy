package providers_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/amitpatole/renvoy/internal/providers"
	"github.com/amitpatole/renvoy/internal/providers/registry"
)

const (
	providersDoc = "../../docs/providers.md"
	matrixBegin  = "<!-- capability-matrix:begin -->"
	matrixEnd    = "<!-- capability-matrix:end -->"
)

// capabilityMatrix renders the capability table and per-provider notes from code.
func capabilityMatrix() string {
	all := registry.All()
	var b strings.Builder
	b.WriteString("| Capability |")
	for _, p := range all {
		fmt.Fprintf(&b, " %s |", p.Name())
	}
	b.WriteString("\n| --- |")
	for range all {
		b.WriteString(" --- |")
	}
	b.WriteString("\n")
	for _, c := range providers.AllCapabilities {
		fmt.Fprintf(&b, "| `%s` |", c)
		for _, p := range all {
			fmt.Fprintf(&b, " %s |", p.Capabilities()[c].Support)
		}
		b.WriteString("\n")
	}
	for _, p := range all {
		fmt.Fprintf(&b, "\n### %s notes\n\n", p.DisplayName())
		for _, info := range p.Capabilities().Ordered() {
			if info.Note != "" {
				fmt.Fprintf(&b, "- `%s` (%s): %s\n", info.ID, info.Support, info.Note)
			}
		}
	}
	return b.String()
}

// TestProvidersDocMatrix keeps docs/providers.md in step with provider code.
// Run `go test ./internal/providers -run TestProvidersDocMatrix -update` after a capability change.
func TestProvidersDocMatrix(t *testing.T) {
	data, err := os.ReadFile(providersDoc)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	start, end := strings.Index(doc, matrixBegin), strings.Index(doc, matrixEnd)
	if start < 0 || end < start {
		t.Fatalf("%s lacks the %s / %s markers", providersDoc, matrixBegin, matrixEnd)
	}
	want := matrixBegin + "\n" + capabilityMatrix() + matrixEnd
	got := doc[start : end+len(matrixEnd)]
	if got == want {
		return
	}
	if *update {
		if err := os.WriteFile(providersDoc, []byte(doc[:start]+want+doc[end+len(matrixEnd):]), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Errorf("%s capability matrix is stale; run go test ./internal/providers -run TestProvidersDocMatrix -update", providersDoc)
}
