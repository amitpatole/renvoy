package pipeline_test

import (
	"strings"
	"testing"

	"github.com/amitpatole/renvoy/internal/diagnostics"
)

func hasWarning(diags diagnostics.List, path, msg string) bool {
	for _, d := range diags {
		if d.Severity == diagnostics.Warning && d.Path == path && strings.Contains(d.Message, msg) {
			return true
		}
	}
	return false
}

// ${git.branch} is attacker-controlled; passing it to a reusable step input is
// the classic workflow injection pattern when the input is evaluated as code.
func TestBranchInStepInputWarns(t *testing.T) {
	src := strings.Replace(minimal, "          - run: make test\n",
		"          - uses: actions/github-script@v7\n            with:\n              script: console.log('${git.branch}')\n", 1)
	_, diags := parse(t, src)
	if diags.HasErrors() {
		t.Fatal(render(diags))
	}
	if !hasWarning(diags, "stages[0].jobs[0].steps[0].with.script", "${git.branch} is controlled by whoever names the branch") {
		t.Fatalf("expected a warning:\n%s", render(diags))
	}
}

// Providers evaluate ${{ }} (GitHub, Azure templates) and $[ ] (Azure runtime
// expressions) anywhere in the generated file, including env and with values.
func TestProviderExpressionSyntaxInValuesWarns(t *testing.T) {
	src := strings.Replace(minimal, "          - run: make test\n",
		"          - run: make test\n            env:\n              TITLE: ${{ github.event.pull_request.title }}\n", 1)
	_, diags := parse(t, src)
	if !hasWarning(diags, "stages[0].jobs[0].steps[0].env.TITLE", "provider expression syntax") {
		t.Fatalf("expected a warning:\n%s", render(diags))
	}
}

// Diagnostics echo user input; escape sequences must not reach the terminal raw.
func TestDiagnosticsEscapeTerminalControls(t *testing.T) {
	src := strings.Replace(minimal, "      - name: unit\n", "      - name: unit\n        \"evil\\e[2J\\u202e\": 1\n", 1)
	_, diags := parse(t, src)
	out := render(diags)
	if strings.ContainsAny(out, "\x1b\u202e") {
		t.Fatalf("raw control characters reached the output: %q", out)
	}
}
