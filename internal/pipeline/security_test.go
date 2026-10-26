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

// Interpreter-control variables act on their value: bash runs $(...) found in
// BASH_ENV at startup, so a branch name there would run code.
func TestControlEnvNames(t *testing.T) {
	withEnv := func(name, value string) string {
		return strings.Replace(minimal, "        steps:\n", "        env:\n          "+name+": '"+value+"'\n        steps:\n", 1)
	}
	_, diags := parse(t, withEnv("BASH_ENV", "${git.branch}"))
	expectError(t, diags, "stages[0].jobs[0].env.BASH_ENV", "cannot be set from ${git.branch}")
	_, diags = parse(t, withEnv("git_config_count", "${git.branch}"))
	expectError(t, diags, "stages[0].jobs[0].env.git_config_count", "cannot be set from ${git.branch}")
	_, diags = parse(t, withEnv("LD_PRELOAD", "./lib.so"))
	if diags.HasErrors() || !hasWarning(diags, "stages[0].jobs[0].env.LD_PRELOAD", "changes how shells or tools behave") {
		t.Fatalf("expected only a warning:\n%s", render(diags))
	}
	_, diags = parse(t, minimal+"variables:\n  BASH_ENV: ./setup.sh\n")
	if !hasWarning(diags, "variables.BASH_ENV", "changes how shells or tools behave") {
		t.Fatalf("expected a warning for a variable:\n%s", render(diags))
	}
	_, diags = parse(t, strings.Replace(minimal, "        steps:\n", "        matrix:\n          PATH: [a, b]\n        steps:\n", 1))
	expectError(t, diags, "stages[0].jobs[0].matrix.PATH", "changes how shells or tools behave")
}

// Provider expression syntax is evaluated by some providers anywhere in the file.
func TestProviderSyntaxWarningsCoverAllValueFields(t *testing.T) {
	src := strings.Replace(minimal, "      - name: unit\n        steps:\n          - run: make test\n",
		"      - name: unit\n        container:\n          image: alpine:3\n          options: --env X=${{ github.head_ref }}\n"+
			"        steps:\n          - name: ${{ github.event.pull_request.title }}\n            run: make test\n", 1) +
		"variables:\n  V1: $(System.AccessToken)\n  V2: \"$[ variables['x'] ]\"\n"
	_, diags := parse(t, src)
	for _, path := range []string{"variables.V1", "variables.V2", "stages[0].jobs[0].container.options", "stages[0].jobs[0].steps[0].name"} {
		if !hasWarning(diags, path, "provider expression syntax") {
			t.Errorf("no provider syntax warning at %s:\n%s", path, render(diags))
		}
	}
}

// Leading dashes turn paths and image names into command-line options.
func TestOptionLikePathsAndImagesAreRejected(t *testing.T) {
	steps := map[string]string{
		"buildImage.context":         "          - buildImage:\n              image: app:1\n              context: --network=host\n",
		"buildImage.dockerfile":      "          - buildImage:\n              image: app:1\n              dockerfile: -f\n",
		"deployKubernetes.manifests": "          - deployKubernetes:\n              manifests: --dry-run=none\n",
		"pushImage.image":            "          - pushImage:\n              image: --all-tags\n",
		"workingDirectory":           "          - run: x\n            workingDirectory: -C\n",
	}
	for field, step := range steps {
		src := strings.Replace(minimal, "          - run: make test\n", step, 1)
		_, diags := parse(t, src)
		expectError(t, diags, "stages[0].jobs[0].steps[0]."+field, "must not start with '-'")
	}
}
