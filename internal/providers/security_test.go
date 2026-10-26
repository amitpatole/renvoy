package providers_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/amitpatole/renvoy/internal/config"
	"github.com/amitpatole/renvoy/internal/pipeline"
	"github.com/amitpatole/renvoy/internal/providers"
	"github.com/amitpatole/renvoy/internal/providers/registry"
	"github.com/amitpatole/renvoy/internal/render"
)

// densePipeline builds stages*perStage jobs where every job of a stage depends on
// every job of the previous stage, the worst case for dependency walks.
func densePipeline(stages, perStage int) string {
	var b strings.Builder
	b.WriteString("apiVersion: renvoy.dev/v1alpha1\nkind: Pipeline\nmetadata: {name: dense}\ntriggers: {push: {}}\n")
	b.WriteString("extensions:\n  bamboo: {projectKey: DENSE, planKey: GRAPH}\nstages:\n")
	for s := 0; s < stages; s++ {
		fmt.Fprintf(&b, "  - name: s%d\n    jobs:\n", s)
		for k := 0; k < perStage; k++ {
			fmt.Fprintf(&b, "      - {name: j%d-%d, when: {branches: [main]}, steps: [{run: x}]}\n", s, k)
		}
	}
	return b.String()
}

// A dense graph at the job limit must stay fast for every provider; before the
// fix, ancestor walks per job made this cubic and a 344 KB file hung for minutes.
func TestDenseGraphAtJobLimitIsFast(t *testing.T) {
	src := densePipeline(10, pipeline.MaxJobs/10)
	start := time.Now()
	res := config.Compile([]byte(src), "renvoy.yaml", registry.Names())
	if res.Pipeline == nil {
		t.Fatalf("does not compile: %v", res.Diagnostics)
	}
	for _, p := range registry.All() {
		p.Validate(res.Pipeline)
		if _, err := p.Render(context.Background(), res.Pipeline, providers.RenderOptions{SourceName: "renvoy.yaml"}); err != nil {
			t.Fatalf("%s: %v", p.Name(), err)
		}
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("validating and rendering %d dense jobs took %v", pipeline.MaxJobs, d)
	}
}

func TestJobLimit(t *testing.T) {
	res := config.Compile([]byte(densePipeline(1, pipeline.MaxJobs+1)), "renvoy.yaml", registry.Names())
	if res.Pipeline != nil || !strings.Contains(fmt.Sprint(res.Diagnostics), "at most 500") {
		t.Fatalf("expected the job limit error, got %v", res.Diagnostics)
	}
}

// The config path is written into every header. A newline or a Java \u000a
// escape in a directory name must not create an uncommented line.
func TestHeaderSourceCannotInjectLines(t *testing.T) {
	for _, source := range []string{
		"evil\nRuntime.getRuntime().exec(\"id\") //renvoy.yaml",
		`x\u000aint pwned = 1; //renvoy.yaml`,
		"x y/renvoy.yaml",
		"x\ry -->renvoy.yaml",
	} {
		for _, style := range []providers.GeneratedFile{
			{Path: "Jenkinsfile", Content: []byte("pipeline {}\n"), Comment: "//"},
			{Path: "pom.xml", Content: []byte("<project/>\n"), Comment: "<!--", CommentEnd: " -->"},
		} {
			out, err := render.Finalize(style, source, nil)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(string(out), "\n")
			src := lines[1]
			if !strings.HasPrefix(src, style.Comment+" Source: ") || strings.ContainsAny(src, "\\\r ") ||
				strings.Count(src, "-->") > strings.Count(style.CommentEnd, "-->") {
				t.Errorf("unsafe header line for %q: %q", source, src)
			}
			if !strings.HasPrefix(lines[2], style.Comment+" Checksum: ") {
				t.Errorf("checksum line moved for %q: %q", source, lines[2])
			}
		}
	}
}
