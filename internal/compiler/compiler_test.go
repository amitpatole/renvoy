package compiler_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/amitpatole/renvoy/internal/config"
	"github.com/amitpatole/renvoy/internal/diagnostics"
	"github.com/amitpatole/renvoy/internal/pipeline"
)

const head = `apiVersion: renvoy.dev/v1alpha1
kind: Pipeline
metadata:
  name: demo
triggers:
  push: {}
stages:
`

func compile(t *testing.T, stages string) config.Result {
	t.Helper()
	return config.Compile([]byte(head+stages), "renvoy.yaml", nil)
}

func expectError(t *testing.T, diags diagnostics.List, path, msg string) {
	t.Helper()
	for _, d := range diags {
		if d.Severity == diagnostics.Error && d.Path == path && strings.Contains(d.Message, msg) {
			return
		}
	}
	var b strings.Builder
	_ = diagnostics.WriteText(&b, diags)
	t.Fatalf("no error at %s containing %q:\n%s", path, msg, b.String())
}

func names(jobs []*pipeline.Job) string {
	var n []string
	for _, j := range jobs {
		n = append(n, j.Name)
	}
	return strings.Join(n, ",")
}

const job = "        steps:\n          - run: x\n"

func TestDefaultStagesAreSequential(t *testing.T) {
	res := compile(t, `  - name: a
    jobs:
      - name: a1
`+job+`  - name: b
    jobs:
      - name: b1
`+job+`      - name: b2
        dependsOn: [b1]
`+job+`  - name: c
    jobs:
      - name: c1
`+job)
	if res.Pipeline == nil {
		t.Fatal(res.Diagnostics)
	}
	p := res.Pipeline
	if got := names(p.Stages[1].Jobs[0].Needs); got != "a1" {
		t.Errorf("b1 needs %q, want a1", got)
	}
	if got := names(p.Stages[1].Jobs[1].Needs); got != "b1" {
		t.Errorf("b2 needs %q, want b1", got)
	}
	// c waits for the final job of b only; b1 is covered through b2.
	if got := names(p.Stages[2].Jobs[0].Needs); got != "b2" {
		t.Errorf("c1 needs %q, want b2", got)
	}
}

func TestExplicitEmptyDependsOnRunsImmediately(t *testing.T) {
	res := compile(t, `  - name: a
    jobs:
      - name: a1
`+job+`  - name: lint
    dependsOn: []
    jobs:
      - name: l1
`+job)
	if res.Pipeline == nil {
		t.Fatal(res.Diagnostics)
	}
	if n := res.Pipeline.Stages[1].Jobs[0].Needs; len(n) != 0 {
		t.Errorf("l1 needs %v, want none", names(n))
	}
}

func TestTopologicalOrderPutsDependenciesFirst(t *testing.T) {
	res := compile(t, `  - name: deploy
    dependsOn: [build]
    jobs:
      - name: d1
`+job+`  - name: build
    dependsOn: []
    jobs:
      - name: b1
`+job)
	if res.Pipeline == nil {
		t.Fatal(res.Diagnostics)
	}
	if got := names(res.Pipeline.OrderedJobs()); got != "b1,d1" {
		t.Errorf("order = %s", got)
	}
}

func TestGraphErrors(t *testing.T) {
	cases := []struct{ name, stages, path, msg string }{
		{"unknown stage", "  - name: a\n    dependsOn: [nope]\n    jobs:\n      - name: a1\n" + job,
			"stages[0].dependsOn[0]", `unknown stage "nope"`},
		{"self stage", "  - name: a\n    dependsOn: [a]\n    jobs:\n      - name: a1\n" + job,
			"stages[0].dependsOn[0]", "depends on itself"},
		{"stage cycle", "  - name: a\n    dependsOn: [b]\n    jobs:\n      - name: a1\n" + job +
			"  - name: b\n    dependsOn: [a]\n    jobs:\n      - name: b1\n" + job,
			"stages[0].dependsOn", "stage dependency cycle: a -> b -> a"},
		{"unknown job", "  - name: a\n    jobs:\n      - name: a1\n        dependsOn: [zz]\n" + job,
			"stages[0].jobs[0].dependsOn[0]", `not a job in stage "a"`},
		{"job cycle", "  - name: a\n    jobs:\n      - name: a1\n        dependsOn: [a2]\n" + job +
			"      - name: a2\n        dependsOn: [a1]\n" + job,
			"stages[0].jobs[0].dependsOn", "job dependency cycle in stage \"a\": a1 -> a2 -> a1"},
		{"artifact without producer", "  - name: a\n    jobs:\n      - name: a1\n        steps:\n          - downloadArtifact:\n              name: dist\n",
			"stages[0].jobs[0].steps[0].downloadArtifact.name", `no job uploads artifact "dist"`},
		{"artifact from parallel job", "  - name: a\n    jobs:\n      - name: a1\n        steps:\n          - uploadArtifact:\n              name: dist\n              paths: [dist/]\n" +
			"      - name: a2\n        steps:\n          - downloadArtifact:\n              name: dist\n",
			"stages[0].jobs[1].steps[0].downloadArtifact.name", "not an upstream dependency"},
		{"duplicate artifact", "  - name: a\n    jobs:\n      - name: a1\n        steps:\n          - uploadArtifact:\n              name: dist\n              paths: [dist/]\n" +
			"          - uploadArtifact:\n              name: dist\n              paths: [out/]\n",
			"stages[0].jobs[0].steps[1].uploadArtifact.name", "already uploaded"},
		{"artifact names differing by case", "  - name: a\n    jobs:\n      - name: a1\n        steps:\n          - uploadArtifact:\n              name: Dist\n              paths: [dist/]\n" +
			"          - uploadArtifact:\n              name: dist\n              paths: [out/]\n",
			"stages[0].jobs[0].steps[1].uploadArtifact.name", "only by case"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := compile(t, tc.stages)
			if res.Pipeline != nil {
				t.Fatal("expected failure")
			}
			expectError(t, res.Diagnostics, tc.path, tc.msg)
		})
	}
}

func TestDiagnosticsCarryPositions(t *testing.T) {
	res := compile(t, "  - name: a\n    dependsOn: [nope]\n    jobs:\n      - name: a1\n"+job)
	for _, d := range res.Diagnostics {
		if d.Path == "stages[0].dependsOn[0]" {
			if d.File != "renvoy.yaml" || d.Line != 9 {
				t.Fatalf("position = %s:%d, want renvoy.yaml:9", d.File, d.Line)
			}
			return
		}
	}
	t.Fatal("diagnostic not found")
}

func TestArtifactFromUpstreamStage(t *testing.T) {
	res := compile(t, "  - name: a\n    jobs:\n      - name: a1\n        steps:\n          - uploadArtifact:\n              name: dist\n              paths: [dist/]\n"+
		"  - name: b\n    jobs:\n      - name: b1\n        steps:\n          - downloadArtifact:\n              name: dist\n")
	if res.Pipeline == nil {
		t.Fatal(res.Diagnostics)
	}
}

// Conditions are repeated on every downstream job by several providers, so their
// total rendered size is bounded (round 3: 846 KB input rendered to 400 MB).
func TestInheritedConditionBudget(t *testing.T) {
	var b strings.Builder
	var pats []string
	for i := 0; i < 100; i++ {
		pats = append(pats, fmt.Sprintf("b%03d-%s", i, strings.Repeat("x", 240)))
	}
	b.WriteString("  - name: s\n    jobs:\n      - name: j0\n        when: {branches: [" + strings.Join(pats, ", ") + "]}\n" + job)
	for i := 1; i < 300; i++ {
		fmt.Fprintf(&b, "      - name: j%d\n        dependsOn: [j%d]\n%s", i, i-1, job)
	}
	res := compile(t, b.String())
	if res.Pipeline != nil {
		t.Fatal("expected the condition budget to reject this pipeline")
	}
	expectError(t, res.Diagnostics, "stages", "conditions repeated on dependent jobs")
}
