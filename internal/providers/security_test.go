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

// customExisting renders one provider for a small pipeline and returns the file
// plus an existing copy whose custom block holds the given lines.
func customExisting(t *testing.T, provider, path string, custom ...string) (providers.GeneratedFile, []byte) {
	t.Helper()
	src := "apiVersion: renvoy.dev/v1alpha1\nkind: Pipeline\nmetadata: {name: demo}\ntriggers: {push: {}}\n" +
		"extensions:\n  bamboo: {projectKey: DEMO, planKey: CUSTOM}\n" +
		"stages:\n  - name: test\n    jobs:\n      - name: unit\n        steps:\n          - uses: checkout\n          - run: make test\n"
	res := config.Compile([]byte(src), "renvoy.yaml", registry.Names())
	if res.Pipeline == nil {
		t.Fatal(res.Diagnostics)
	}
	p, _ := registry.Get(provider)
	files, err := p.Render(context.Background(), res.Pipeline, providers.RenderOptions{SourceName: "renvoy.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Path != path {
			continue
		}
		out, err := render.Finalize(f, "renvoy.yaml", nil)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(out), "\n")
		for i, l := range lines {
			if strings.Contains(l, "Provider-specific content retained during regeneration.") {
				lines = append(append(append([]string{}, lines[:i]...), custom...), lines[i+1:]...)
				return f, []byte(strings.Join(lines, "\n"))
			}
		}
		if path == "bamboo-specs/src/main/java/specs/PlanSpec.java" {
			for i, l := range lines {
				if strings.TrimSpace(l) == "// renvoy:begin custom" {
					end := i + 1
					for strings.TrimSpace(lines[end]) != "// renvoy:end custom" {
						end++
					}
					lines = append(append(append([]string{}, lines[:i+1]...), custom...), lines[end:]...)
					return f, []byte(strings.Join(lines, "\n"))
				}
			}
		}
		t.Fatalf("%s has no custom block", path)
	}
	t.Fatalf("%s did not render %s", provider, path)
	return providers.GeneratedFile{}, nil
}

// Round 4: custom blocks may only add content that does not change how the
// generated jobs run, in a form every downstream parser reads the same way.
func TestCustomBlockPoliciesPerProvider(t *testing.T) {
	java := "bamboo-specs/src/main/java/specs/PlanSpec.java"
	cases := []struct {
		name, provider, path string
		lines                []string
		ok                   bool
	}{
		{"gitlab new job", "gitlab", ".gitlab-ci.yml", []string{"extra:", "  script: [make extra]"}, true},
		{"gitlab hidden template", "gitlab", ".gitlab-ci.yml", []string{".base:", "  tags: [docker]"}, true},
		{"gitlab default applies to all jobs", "gitlab", ".gitlab-ci.yml", []string{"default:", "  before_script: [echo MARKER]"}, false},
		{"gitlab include", "gitlab", ".gitlab-ci.yml", []string{"include:", "  - local: other.yml"}, false},
		{"gitlab yaml 1.1 key", "gitlab", ".gitlab-ci.yml", []string{"yes:", "  script: [echo MARKER]"}, false},
		{"gitlab NEL line break", "gitlab", ".gitlab-ci.yml", []string{"extra: a\u0085unit: replaced"}, false},
		{"gitlab LS line break", "gitlab", ".gitlab-ci.yml", []string{"extra: a\u2028unit: replaced"}, false},
		{"github concurrency", "github-actions", ".github/workflows/demo.yml", []string{"concurrency:", "  group: ci"}, true},
		{"github env applies to all jobs", "github-actions", ".github/workflows/demo.yml", []string{"env:", "  MARKER: 1"}, false},
		{"github defaults", "github-actions", ".github/workflows/demo.yml", []string{"defaults:", "  run:", "    working-directory: x"}, false},
		{"azure name", "azure-devops", "azure-pipelines.yml", []string{"name: $(Date:yyyyMMdd)$(Rev:.r)"}, true},
		{"azure pool", "azure-devops", "azure-pipelines.yml", []string{"pool:", "  name: other"}, false},
		{"azure extends", "azure-devops", "azure-pipelines.yml", []string{"extends:", "  template: t.yml"}, false},
		{"bamboo notifications", "bamboo", "bamboo-specs/bamboo.yaml", []string{"notifications: []"}, true},
		{"bamboo docker default", "bamboo", "bamboo-specs/bamboo.yaml", []string{"docker: alpine"}, false},
		{"bamboo branch overrides", "bamboo", "bamboo-specs/bamboo.yaml", []string{"branch-overrides: []"}, false},
		{"jenkins post block", "jenkins", "Jenkinsfile", []string{"    post {", "        always { echo 'done' }", "    }"}, true},
		{"jenkins interpolation", "jenkins", "Jenkinsfile", []string{"    post { always { echo \"${env.X}\" } }"}, false},
		{"jenkins lone CR", "jenkins", "Jenkinsfile", []string{"    // note\r } node { echo 'x' } pipeline {"}, false},
		{"java helper method", "bamboo-java", java, []string{"    Plan customize(Plan plan) {", "        return plan.description(\"x\");", "    }"}, true},
		{"java lone CR (javac ends the comment)", "bamboo-java", java, []string{"    Plan customize(Plan plan) { return plan; }", "    // note\r } class Extra { static int marker = 42;", "    static int y = 1;"}, false},
		{"pom doctype", "bamboo-java", "bamboo-specs/pom.xml", []string{"<!DOCTYPE x [<!ENTITY a \"b\">]>"}, false},
		{"pom dependency", "bamboo-java", "bamboo-specs/pom.xml", []string{"    <dependency><groupId>a</groupId></dependency>"}, true},
		{"pom attlist", "bamboo-java", "bamboo-specs/pom.xml", []string{"    <!ATTLIST dependency x CDATA #IMPLIED>"}, false},
		{"pom cdata and comment", "bamboo-java", "bamboo-specs/pom.xml", []string{"    <!-- note --><dependency><groupId><![CDATA[a]]></groupId></dependency>"}, true},
		{"gitlab BOM before key", "gitlab", ".gitlab-ci.yml", []string{"\ufeffnewjob:", "  script: [echo MARKER]"}, false},
		{"gitlab float key", "gitlab", ".gitlab-ci.yml", []string{".inf:", "  script: [echo MARKER]"}, false},
		{"gitlab trailing-space key", "gitlab", ".gitlab-ci.yml", []string{"\"variables \":", "  X: MARKER"}, false},
		{"jenkins pipeline options", "jenkins", "Jenkinsfile", []string{"    options { retry(2) }"}, false},
		{"jenkins pipeline environment", "jenkins", "Jenkinsfile", []string{"    environment { X = 'MARKER' }"}, false},
		{"jenkins nested options in post is fine", "jenkins", "Jenkinsfile", []string{"    post { always { echo 'options { }' } }"}, true},
		{"jenkins quoted directive name", "jenkins", "Jenkinsfile", []string{"    'options' { retry(2) }"}, false},
		{"jenkins line continuation", "jenkins", "Jenkinsfile", []string{"    options \\", "    { retry(2) }"}, false},
		{"jenkins single-quoted shell variable", "jenkins", "Jenkinsfile", []string{"    post { always { sh 'echo ${VAR}' } }"}, true},
		{"jenkins two post-like sections only", "jenkins", "Jenkinsfile", []string{"    post { failure { echo 'x' } }"}, true},
		{"java invalid utf-8", "bamboo-java", java, []string{"    Plan customize(Plan plan) { return plan; }", "    // x\xff\xfe"}, false},
		{"gitlab tab in value", "gitlab", ".gitlab-ci.yml", []string{"extra:", "  script: [\"a\tb\"]", "  variables: {X: \"a\tb\"}"}, false},
		{"gitlab pages job", "gitlab", ".gitlab-ci.yml", []string{"pages:", "  script: [make site]", "  artifacts: {paths: [public/]}"}, true},
		{"jenkins string-named directive", "jenkins", "Jenkinsfile", []string{"    'agent' 'any'"}, false},
		{"jenkins escaped dollar in double quotes", "jenkins", "Jenkinsfile", []string{"    post { always { sh \"echo \\${VAR}\" } }"}, true},
		{"pom unbound prefix", "bamboo-java", "bamboo-specs/pom.xml", []string{"    <a:b/>"}, false},
		{"pom duplicate attribute", "bamboo-java", "bamboo-specs/pom.xml", []string{"    <dependency x=\"1\" x=\"2\"/>"}, false},
		{"pom control character", "bamboo-java", "bamboo-specs/pom.xml", []string{"    <!-- a\x01 -->"}, false},
		{"pom leading colon name", "bamboo-java", "bamboo-specs/pom.xml", []string{"    <:a/>"}, false},
		{"pom trailing colon name", "bamboo-java", "bamboo-specs/pom.xml", []string{"    <a:/>"}, false},
		{"pom surrogate char ref", "bamboo-java", "bamboo-specs/pom.xml", []string{"    <dependency>&#xD800;</dependency>"}, false},
		{"pom legal char ref", "bamboo-java", "bamboo-specs/pom.xml", []string{"    <dependency><groupId>&#x41;</groupId></dependency>"}, true},
		{"java division", "bamboo-java", java, []string{"    Plan customize(Plan plan) { int x = 4 / 2; return plan; }"}, true},
		{"jenkins division still refused", "jenkins", "Jenkinsfile", []string{"    post { always { echo 'x' } } // a", "    def y = 4 / 2"}, false},
	}
	for _, c := range cases {
		f, existing := customExisting(t, c.provider, c.path, c.lines...)
		_, err := render.Finalize(f, "renvoy.yaml", existing)
		if c.ok && err != nil {
			t.Errorf("%s: legitimate custom content was refused: %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: custom content was accepted", c.name)
		}
	}
}

// Round 7: decoding custom YAML into maps checks duplicate keys in quadratic
// time, so the custom part is bounded on its node tree first.
func TestLargeCustomYAMLFailsFast(t *testing.T) {
	var lines []string
	for i := 0; i < 20000; i++ {
		lines = append(lines, fmt.Sprintf("k%d: {script: [a]}", i))
	}
	f, existing := customExisting(t, "gitlab", ".gitlab-ci.yml", lines...)
	start := time.Now()
	if _, err := render.Finalize(f, "renvoy.yaml", existing); err == nil {
		t.Fatal("an oversized custom block was accepted")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("refusing an oversized custom block took %v", d)
	}
}
