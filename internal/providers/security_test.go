package providers_test

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/amitpatole/renvoy/internal/diagnostics"

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
		{"pom colon in comment", "bamboo-java", "bamboo-specs/pom.xml", []string{"    <!-- see https://example.com/docs?a=b and -Dfoo:bar=1 -->"}, true},
		{"pom colon in text", "bamboo-java", "bamboo-specs/pom.xml", []string{"    <dependency><groupId>ratio x:y = 3</groupId></dependency>"}, true},
		{"pom colon and char ref in CDATA", "bamboo-java", "bamboo-specs/pom.xml", []string{"    <dependency><groupId><![CDATA[ a:b = c &#0; ]]></groupId></dependency>"}, true},
		{"pom char ref in comment", "bamboo-java", "bamboo-specs/pom.xml", []string{"    <!-- &#1; -->"}, true},
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

// Round 12: rendering at the entry and schedule limits stays fast for every
// provider, including when a custom block triggers the combined-file checks.
func TestRenderingAtLimitsIsFast(t *testing.T) {
	var b strings.Builder
	b.WriteString("apiVersion: renvoy.dev/v1alpha1\nkind: Pipeline\nmetadata: {name: big}\ntriggers:\n  push: {}\n  schedules:\n")
	for s := 0; s < pipeline.MaxSchedules; s++ {
		b.WriteString("    - cron: \"0 3 * * *\"\n      branches: [")
		for i := 0; i < pipeline.MaxPatterns; i++ {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "s%d-b%d", s, i)
		}
		b.WriteString("]\n")
	}
	b.WriteString("extensions:\n  bamboo: {projectKey: BIG, planKey: LIMITS}\nstages:\n  - name: s\n    jobs:\n      - name: j\n        env:\n")
	for i := 0; i < pipeline.MaxVarsEntries; i++ {
		fmt.Fprintf(&b, "          E%d: v\n", i)
	}
	b.WriteString("        steps:\n          - uses: checkout\n          - run: x\n")
	res := config.Compile([]byte(b.String()), "renvoy.yaml", registry.Names())
	if res.Pipeline == nil {
		t.Fatalf("does not compile: %v", res.Diagnostics)
	}
	start := time.Now()
	for _, p := range registry.All() {
		files, err := p.Render(context.Background(), res.Pipeline, providers.RenderOptions{SourceName: "renvoy.yaml"})
		if err != nil {
			t.Fatalf("%s: %v", p.Name(), err)
		}
		for _, f := range files {
			out, err := render.Finalize(f, "renvoy.yaml", nil)
			if err != nil {
				continue
			}
			// Re-finalize against itself to exercise the custom-block path.
			if _, err := render.Finalize(f, "renvoy.yaml", out); err != nil {
				t.Fatalf("%s %s: %v", p.Name(), f.Path, err)
			}
		}
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("rendering at the limits took %v", d)
	}
}

// Round 13: names Groovy cannot assign in an environment block (keywords and
// default-imported class names) are rejected for Jenkins instead of producing
// a Jenkinsfile that does not compile; other providers accept them.
func TestJenkinsRejectsGroovyReservedNames(t *testing.T) {
	src := `apiVersion: renvoy.dev/v1alpha1
kind: Pipeline
metadata: {name: names}
triggers: {push: {}}
secrets: [UUID]
variables: {if: v, URL: v, PATH_PREFIX: v}
stages:
  - name: s
    jobs:
      - name: j
        env: {Map: v, it: v}
        steps:
          - run: echo ${secrets.UUID}
`
	res := config.Compile([]byte(src), "renvoy.yaml", registry.Names())
	if res.Pipeline == nil {
		t.Fatalf("does not compile: %v", res.Diagnostics)
	}
	jk, _ := registry.Get("jenkins")
	var got []string
	for _, d := range jk.Validate(res.Pipeline) {
		if d.Severity == diagnostics.Error {
			got = append(got, d.Path)
		}
	}
	want := []string{"variables.if", "variables.URL", "stages[0].jobs[0].env.Map", "secrets[0]"}
	if !slices.Equal(got, want) {
		t.Fatalf("errors at %v, want %v", got, want)
	}
	gh, _ := registry.Get("github-actions")
	for _, d := range gh.Validate(res.Pipeline) {
		if d.Severity == diagnostics.Error {
			t.Fatalf("github-actions: %v", d)
		}
	}
}

// round14 compiles src and returns the diagnostics of one provider.
func round14(t *testing.T, provider, src string) diagnostics.List {
	t.Helper()
	res := config.Compile([]byte(src), "renvoy.yaml", registry.Names())
	if res.Pipeline == nil {
		t.Fatalf("does not compile: %v", res.Diagnostics)
	}
	p, _ := registry.Get(provider)
	return p.Validate(res.Pipeline)
}

func hasDiag(diags diagnostics.List, sev diagnostics.Severity, path, msg string) bool {
	return slices.ContainsFunc(diags, func(d diagnostics.Diagnostic) bool {
		return d.Severity == sev && d.Path == path && strings.Contains(d.Message, msg)
	})
}

const round14Base = `apiVersion: renvoy.dev/v1alpha1
kind: Pipeline
metadata: {name: r14}
triggers: {push: {}}
extensions:
  bamboo: {projectKey: PROJ, planKey: PLAN}
stages:
  - name: s
    jobs:
      - name: j
        steps: [{run: echo MARKER}]
`

// Round 14: provider limits and reserved names the target platform enforces.
func TestRound14ProviderRules(t *testing.T) {
	job := func(extra string) string {
		return strings.Replace(round14Base, "        steps:", extra+"        steps:", 1)
	}
	if d := round14(t, "github-actions", job("        matrix: {Include: [a, b], v: [x]}\n")); !hasDiag(d, diagnostics.Error, "stages[0].jobs[0].matrix.Include", "reserves the matrix key") {
		t.Errorf("github include axis: %v", d)
	}
	if d := round14(t, "github-actions", job("        env: {GITHUB_REF: x}\n")); !hasDiag(d, diagnostics.Warning, "stages[0].jobs[0].env.GITHUB_REF", "GITHUB_*") {
		t.Errorf("github default variable: %v", d)
	}
	var axes []string
	for i := 0; i < 3; i++ {
		axes = append(axes, fmt.Sprintf("a%d: [%s]", i, strings.Join(strings.Split("v1 v2 v3 v4 v5 v6", " "), ", ")))
	}
	if d := round14(t, "gitlab", job("        matrix: {"+strings.Join(axes, ", ")+"}\n")); !hasDiag(d, diagnostics.Error, "stages[0].jobs[0].matrix", "at most 200") {
		t.Errorf("gitlab 216-job matrix: %v", d)
	}
	var wide strings.Builder
	for i := 0; i < 51; i++ {
		fmt.Fprintf(&wide, "      - name: w%d\n        steps: [{run: echo MARKER}]\n", i)
	}
	src := strings.Replace(round14Base, "      - name: j\n", wide.String()+"  - name: t\n    jobs:\n      - name: j\n", 1)
	if d := round14(t, "gitlab", src); !hasDiag(d, diagnostics.Warning, "stages[1].jobs[0]", "needs 51 jobs") {
		t.Errorf("gitlab needs limit: %v", d)
	}
	for _, pkg := range []string{"java.lang", "javax.sql", "jdk.internal", "com.sun.net", "org.w3c.dom", "java"} {
		src := strings.Replace(round14Base, "planKey: PLAN}", "planKey: PLAN, javaPackage: "+pkg+"}", 1)
		if d := round14(t, "bamboo-java", src); !hasDiag(d, diagnostics.Error, "extensions.bamboo.javaPackage", "namespace") {
			t.Errorf("bamboo-java package %s: %v", pkg, d)
		}
	}
	for label, ok := range map[string]bool{"linux": true, "linux && docker": true, "!windows && (x64 || arm-64)": true, "a.b_c-d": true,
		"a &&": false, "a ||| b": false, "a)": false, "(a": false, "a & b": false, "a b": false, "!": false, "a && || b": false} {
		src := strings.Replace(round14Base, "extensions:\n", "extensions:\n  jenkins: {agentLabel: \""+label+"\"}\n", 1)
		d := round14(t, "jenkins", src)
		if got := !hasDiag(d, diagnostics.Error, "extensions.jenkins.agentLabel", "invalid agent label"); got != ok {
			t.Errorf("jenkins label %q: accepted=%v, want %v", label, got, ok)
		}
	}
}

// Round 14: Azure matrix leg names stay unique when values differ only by case.
func TestAzureLegNamesIgnoreCase(t *testing.T) {
	src := strings.Replace(round14Base, "        steps:", "        matrix: {ax: [A, a, a_2]}\n        steps:", 1)
	res := config.Compile([]byte(src), "renvoy.yaml", registry.Names())
	az, _ := registry.Get("azure-devops")
	files, err := az.Render(context.Background(), res.Pipeline, providers.RenderOptions{SourceName: "renvoy.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(files[0].Content, &doc); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(files[0].Content), "\n") {
		if name, ok := strings.CutSuffix(strings.TrimSpace(line), ":"); ok && strings.HasPrefix(name, "ax_") {
			if seen[strings.ToLower(name)] {
				t.Fatalf("leg %q repeats ignoring case:\n%s", name, files[0].Content)
			}
			seen[strings.ToLower(name)] = true
		}
	}
	if len(seen) != 3 {
		t.Fatalf("legs %v:\n%s", seen, files[0].Content)
	}
}

// Round 15: Azure does not pass variables named with its reserved prefixes
// (endpoint, input, secret, path, securefile, in any case) to scripts.
func TestAzureReservedVariablePrefixes(t *testing.T) {
	src := strings.Replace(round14Base, "extensions:\n", "secrets: [SecretKey]\nvariables: {INPUT_DIR: x, path_prefix: y, OUTPUT_DIR: z}\nextensions:\n", 1)
	src = strings.Replace(src, "        steps:", "        env: {EndpointUrl: u}\n        matrix: {securefile_name: [a]}\n        steps:", 1)
	d := round14(t, "azure-devops", src)
	for _, path := range []string{"secrets[0]", "variables.INPUT_DIR", "variables.path_prefix", "stages[0].jobs[0].env.EndpointUrl", "stages[0].jobs[0].matrix.securefile_name"} {
		if !hasDiag(d, diagnostics.Error, path, "Azure Pipelines reserves") {
			t.Errorf("%s not rejected: %v", path, d)
		}
	}
	if hasDiag(d, diagnostics.Error, "variables.OUTPUT_DIR", "reserves") {
		t.Error("OUTPUT_DIR rejected")
	}
	if g := round14(t, "github-actions", src); hasDiag(g, diagnostics.Error, "variables.INPUT_DIR", "reserves") {
		t.Error("github-actions applies the Azure rule")
	}
}

// Round 17: GitHub's 5-minute schedule floor and 500 artifacts per job, and
// Bamboo's refusal of plain-text values in password-like variables.
func TestRound17ProviderRules(t *testing.T) {
	sched := func(cron string) string {
		return strings.Replace(round14Base, "triggers: {push: {}}", "triggers:\n  push: {}\n  schedules: [{cron: \""+cron+"\", branches: [main]}]", 1)
	}
	for cron, warn := range map[string]bool{"*/1 * * * *": true, "0-59/2 * * * *": true, "0,58 * * * *": true, "*/5 * * * *": false, "0 3 * * *": false, "0,30 * * * *": false} {
		d := round14(t, "github-actions", sched(cron))
		if got := hasDiag(d, diagnostics.Warning, "triggers.schedules[0].cron", "every 5 minutes"); got != warn {
			t.Errorf("%s: warned=%v, want %v", cron, got, warn)
		}
	}
	var steps strings.Builder
	for i := 0; i < 501; i++ {
		fmt.Fprintf(&steps, "          - uploadArtifact: {name: a%d, paths: [dist/]}\n", i)
	}
	src := strings.Replace(round14Base, "        steps: [{run: echo MARKER}]\n", "        steps:\n"+steps.String(), 1)
	if d := round14(t, "github-actions", src); !hasDiag(d, diagnostics.Error, "stages[0].jobs[0].steps", "at most 500") {
		t.Errorf("501 artifacts: %v", d)
	}
	src = strings.Replace(round14Base, "extensions:\n", "variables: {DB_PASSWORD: x, sshKeyFile: y, PLAIN: z}\nextensions:\n", 1)
	for _, prov := range []string{"bamboo", "bamboo-java"} {
		d := round14(t, prov, src)
		if !hasDiag(d, diagnostics.Error, "variables.DB_PASSWORD", "as passwords") || !hasDiag(d, diagnostics.Error, "variables.sshKeyFile", "as passwords") || hasDiag(d, diagnostics.Error, "variables.PLAIN", "as passwords") {
			t.Errorf("%s: %v", prov, d)
		}
	}
}

// Round 18: hosted-runner timeouts, GitLab predefined variable prefixes, and
// Bamboo requirements with blank keys or match values.
func TestRound18ProviderRules(t *testing.T) {
	src := strings.Replace(round14Base, "        steps:", "        timeoutMinutes: 720\n        steps:", 1)
	for _, prov := range []string{"github-actions", "azure-devops"} {
		if d := round14(t, prov, src); !hasDiag(d, diagnostics.Warning, "stages[0].jobs[0].timeoutMinutes", "360 minutes") {
			t.Errorf("%s timeout: %v", prov, d)
		}
	}
	src = strings.Replace(round14Base, "extensions:\n", "variables: {CI_ENVIRONMENT_URL: x, MY_URL: y}\nextensions:\n", 1)
	d := round14(t, "gitlab", src)
	if !hasDiag(d, diagnostics.Warning, "variables.CI_ENVIRONMENT_URL", "predefined") || hasDiag(d, diagnostics.Warning, "variables.MY_URL", "predefined") {
		t.Errorf("gitlab predefined: %v", d)
	}
	for req, ok := range map[string]bool{"system.docker.executable": true, "os=Linux.*": true, " ": false, " =x": false, "k= ": false, "k=\\t": false, "k=\\u001f": false} {
		src := strings.Replace(round14Base, "planKey: PLAN}", "planKey: PLAN, requirements: {linux: [\""+req+"\"]}}", 1)
		for _, prov := range []string{"bamboo", "bamboo-java"} {
			if got := !hasDiag(round14(t, prov, src), diagnostics.Error, "extensions.bamboo.requirements.linux[0]", "invalid requirement"); got != ok {
				t.Errorf("%s requirement %q: accepted=%v, want %v", prov, req, got, ok)
			}
		}
	}
}

func TestRound19ProviderRules(t *testing.T) {
	job := func(extra string) string {
		return strings.Replace(round14Base, "        steps:", extra+"        steps:", 1)
	}
	if d := round14(t, "azure-devops", job("        environment: {name: prod.eu}\n")); !hasDiag(d, diagnostics.Error, "stages[0].jobs[0].environment.name", "resource") {
		t.Errorf("azure environment dot: %v", d)
	}
	for _, opts := range []string{"--network host", "--cpus 1 --net=none", "--entrypoint=/bin/sh"} {
		d := round14(t, "github-actions", job("        container: {image: alpine, options: \""+opts+"\"}\n"))
		if !hasDiag(d, diagnostics.Error, "stages[0].jobs[0].container.options", "does not support") {
			t.Errorf("github options %q: %v", opts, d)
		}
	}
	if d := round14(t, "github-actions", job("        container: {image: alpine, options: \"--network-alias db --cpus 1\"}\n")); hasDiag(d, diagnostics.Error, "stages[0].jobs[0].container.options", "does not support") {
		t.Errorf("github --network-alias: %v", d)
	}
	for req, ok := range map[string]bool{"k=1.8.*": true, "k=(?i)jdk": true, "k=[a-z]+|x{2,3}?": true, "k=[": false, "k=(": false,
		"k=*": false, "k=a{2,1}": false, "k=(?<x": false, "k=${bamboo.x}": false, "k=[z-a]": false} {
		src := strings.Replace(round14Base, "planKey: PLAN}", "planKey: PLAN, requirements: {linux: [\""+req+"\"]}}", 1)
		for _, prov := range []string{"bamboo", "bamboo-java"} {
			if got := !hasDiag(round14(t, prov, src), diagnostics.Error, "extensions.bamboo.requirements.linux[0]", "regular expression"); got != ok {
				t.Errorf("%s requirement %q: accepted=%v, want %v", prov, req, got, ok)
			}
		}
	}
}

func render19(t *testing.T, provider, src string) string {
	t.Helper()
	res := config.Compile([]byte(src), "renvoy.yaml", registry.Names())
	if res.Pipeline == nil {
		t.Fatalf("does not compile: %v", res.Diagnostics)
	}
	p, _ := registry.Get(provider)
	if d := p.Validate(res.Pipeline); d.HasErrors() {
		t.Fatalf("%s: %v", provider, d)
	}
	files, err := p.Render(context.Background(), res.Pipeline, providers.RenderOptions{SourceName: "renvoy.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, f := range files {
		out.Write(f.Content)
	}
	return out.String()
}

func TestAzureDeployJobIsRenamed(t *testing.T) {
	src := strings.Replace(round14Base, "      - name: j\n        steps: [{run: echo MARKER}]\n",
		"      - name: deploy\n        environment: {name: prod}\n        steps: [{run: echo MARKER}]\n      - name: after\n        dependsOn: [deploy]\n        steps: [{run: echo MARKER}]\n", 1)
	out := render19(t, "azure-devops", src)
	if !strings.Contains(out, "deployment: deploy_\n") || !strings.Contains(out, "dependsOn: [deploy_]") {
		t.Fatalf("deploy job not renamed:\n%s", out)
	}
}

func TestAzureLegNamesStartWithLetterAndFit(t *testing.T) {
	long := strings.Repeat("v", 60)
	src := strings.Replace(round14Base, "        steps:", "        matrix: {_x: ["+long+"1, "+long+"2, b], _y: ["+long+"]}\n        steps:", 1)
	var doc struct {
		Stages []struct {
			Jobs []struct {
				Strategy struct {
					Matrix map[string]any `yaml:"matrix"`
				} `yaml:"strategy"`
			} `yaml:"jobs"`
		} `yaml:"stages"`
	}
	out := render19(t, "azure-devops", src)
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	legs := doc.Stages[0].Jobs[0].Strategy.Matrix
	if len(legs) != 3 {
		t.Fatalf("legs %v:\n%s", legs, out)
	}
	for name := range legs {
		if c := name[0]; len(name) > 100 || (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			t.Errorf("invalid leg name %q", name)
		}
	}
}

func TestBambooJavaSplitsLongStrings(t *testing.T) {
	src := strings.Replace(round14Base, "steps: [{run: echo MARKER}]", "steps: [{run: \"echo "+strings.Repeat("é", 40000)+"\"}]", 1)
	out := render19(t, "bamboo-java", src)
	if !strings.Contains(out, `String.join("", "echo `) {
		t.Fatal("long script was not split")
	}
	for _, lit := range regexp.MustCompile(`"(?:[^"\\]|\\.)*"`).FindAllString(out, -1) {
		// Each \u00e9 escape is two bytes in the class file.
		if n := len(strings.ReplaceAll(lit, `\u00e9`, "é")); n > 65535 {
			t.Fatalf("a %d-byte string constant remains", n)
		}
	}
}

func TestRound20ProviderRules(t *testing.T) {
	steps := func(s string) string {
		return strings.Replace(round14Base, "        steps: [{run: echo MARKER}]\n", "        steps:\n"+s, 1)
	}
	two := steps("          - publishTestResults: {path: r/a.xml}\n          - publishTestResults: {path: r/b.xml}\n")
	if out := render19(t, "github-actions", two); !strings.Contains(out, "name: test-results-j\n") || !strings.Contains(out, "name: test-results-j.2\n") {
		t.Errorf("test result artifacts are not unique:\n%s", out)
	}
	clash := steps("          - uploadArtifact: {name: test-results-j, paths: [dist/]}\n          - publishTestResults: {path: r/a.xml}\n")
	if d := round14(t, "github-actions", clash); !hasDiag(d, diagnostics.Error, "stages[0].jobs[0].steps[0].uploadArtifact.name", "reserved for test results") {
		t.Errorf("github reserved artifact name: %v", d)
	}
	for _, opts := range []string{`'--network' host`, `\"--entrypoint\"=sh`} {
		src := strings.Replace(round14Base, "        steps:", "        container: {image: alpine, options: \""+opts+"\"}\n        steps:", 1)
		if d := round14(t, "github-actions", src); !hasDiag(d, diagnostics.Error, "stages[0].jobs[0].container.options", "does not support") {
			t.Errorf("github quoted options %q: %v", opts, d)
		}
	}
	for cron, ok := range map[string]bool{"0 3 1 * 1": false, "0 3 1-5/2 * 1-5": false, "0 3 1 * *": true, "0 3 * * 1": true} {
		src := strings.Replace(round14Base, "push: {}", "schedules: [{cron: \""+cron+"\", branches: [main]}]", 1)
		if got := !hasDiag(round14(t, "jenkins", src), diagnostics.Error, "triggers.schedules[0].cron", "both the day of month"); got != ok {
			t.Errorf("jenkins cron %q: accepted=%v, want %v", cron, got, ok)
		}
	}
	for req, ok := range map[string]bool{"k=(?i)a": true, "k=(?x)(a#)": false, "k=(?x) *": false,
		"k=" + strings.Repeat("a", 256): false, "k=" + strings.Repeat("(", 33) + "a" + strings.Repeat(")", 33): false,
		"k=" + strings.Repeat("(", 32) + "a" + strings.Repeat(")", 32): true} {
		src := strings.Replace(round14Base, "planKey: PLAN}", "planKey: PLAN, requirements: {linux: [\""+req+"\"]}}", 1)
		if got := !hasDiag(round14(t, "bamboo", src), diagnostics.Error, "extensions.bamboo.requirements.linux[0]", "regular expression"); got != ok {
			t.Errorf("bamboo requirement %.40q: accepted=%v, want %v", req, got, ok)
		}
	}
}

func TestBambooJobKeysFitInForty(t *testing.T) {
	long := strings.Repeat("j", 61)
	src := strings.Replace(round14Base, "      - name: j\n        steps: [{run: echo MARKER}]\n",
		"      - name: "+long+"a\n        steps: [{run: echo MARKER}]\n      - name: "+long+"b\n        steps: [{run: echo MARKER}]\n", 1)
	out := render19(t, "bamboo-java", src)
	keys := regexp.MustCompile(`new BambooKey\("([A-Z0-9]+)"\)`).FindAllStringSubmatch(out, -1)
	if len(keys) < 4 {
		t.Fatalf("keys %v", keys)
	}
	for _, k := range keys {
		if len(k[1]) > 40 {
			t.Errorf("key %q is longer than 40 characters", k[1])
		}
	}
}

func TestBambooJavaStaysWithinClassLimits(t *testing.T) {
	var many strings.Builder
	for i := 0; i < 1100; i++ {
		fmt.Fprintf(&many, "          - run: echo MARKER %d\n", i)
	}
	src := strings.Replace(round14Base, "        steps: [{run: echo MARKER}]\n", "        steps:\n"+many.String(), 1)
	if out := render19(t, "bamboo-java", src); !strings.Contains(out, ".tasks(tasks2())") || !strings.Contains(out, "static ScriptTask task1100()") {
		t.Error("long task lists are not grouped")
	}
	var reqs []string
	for i := 0; i < 6000; i++ {
		reqs = append(reqs, fmt.Sprintf("cap%d=v%d", i, i))
	}
	src = strings.Replace(round14Base, "planKey: PLAN}", "planKey: PLAN, requirements: {linux: ["+strings.Join(reqs, ", ")+"]}}", 1)
	if d := round14(t, "bamboo-java", src); !hasDiag(d, diagnostics.Error, "stages[0].jobs[0]", "too large for Bamboo Java Specs") {
		t.Errorf("oversized job: %v", d)
	}
}

func TestRound21ProviderRules(t *testing.T) {
	job := func(extra string) string {
		return strings.Replace(round14Base, "        steps:", extra+"        steps:", 1)
	}
	if out := render19(t, "jenkins", job("        when: {branches: [\"release*\"]}\n")); !strings.Contains(out, "not { buildingTag() }") || !strings.Contains(out, "not { changeRequest() }") {
		t.Errorf("jenkins branch condition matches tags and pull requests:\n%s", out)
	}
	if out := render19(t, "azure-devops", job("        env: {BRANCH_NAME: \"${git.branch}\"}\n")); !strings.Contains(out, "'refs/tags/', '')") {
		t.Errorf("azure branch keeps refs/tags/:\n%s", out)
	}
	src := strings.Replace(round14Base, "push: {}", "pullRequest: {}", 1)
	for _, prov := range []string{"bamboo", "bamboo-java"} {
		if d := round14(t, prov, src); !hasDiag(d, diagnostics.Warning, "triggers.pullRequest", "also builds every push") {
			t.Errorf("%s pull-request-only: %v", prov, d)
		}
	}
	manual := strings.Replace(round14Base, "push: {}", "manual: {}", 1)
	if out := render19(t, "bamboo", manual); !strings.Contains(out, "create: manually") {
		t.Errorf("bamboo plan branches not manual:\n%s", out)
	}
	if out := render19(t, "bamboo-java", manual); !strings.Contains(out, "createManually()") {
		t.Error("bamboo-java plan branches not manual")
	}
	win := strings.Replace(round14Base, "        steps: [{run: echo MARKER}]", "        runsOn: windows\n        steps: [{run: echo MARKER, shell: sh}]", 1)
	if d := round14(t, "github-actions", win); !hasDiag(d, diagnostics.Error, "stages[0].jobs[0].steps[0].shell", "only on Linux and macOS") {
		t.Errorf("github sh on windows: %v", d)
	}
	uses := strings.Replace(round14Base, "        steps: [{run: echo MARKER}]\n",
		"        steps:\n          - uses: actions/upload-artifact@v4\n            with: {name: test-results-j, path: x}\n          - publishTestResults: {path: r/a.xml}\n", 1)
	if d := round14(t, "github-actions", uses); !hasDiag(d, diagnostics.Error, "stages[0].jobs[0].steps[0].with.name", "reserved for test results") {
		t.Errorf("github raw upload-artifact: %v", d)
	}
}

func TestRound22ReservedArtifactInputs(t *testing.T) {
	for _, c := range []struct{ uses, with, key string }{
		{"actions/upload-artifact@v4", "{NAME: test-results-j, path: x}", "NAME"},
		{"actions/upload-artifact/merge@v4", "{name: test-results-j}", "name"},
		{"actions/upload-artifact@v4", "{name: \" Test-Results-j\", path: x}", "name"},
	} {
		src := strings.Replace(round14Base, "        steps: [{run: echo MARKER}]\n",
			"        steps:\n          - uses: "+c.uses+"\n            with: "+c.with+"\n          - publishTestResults: {path: r/a.xml}\n", 1)
		if d := round14(t, "github-actions", src); !hasDiag(d, diagnostics.Error, "stages[0].jobs[0].steps[0].with."+c.key, "reserved for test results") {
			t.Errorf("%s %s: %v", c.uses, c.with, d)
		}
	}
}

func TestRound23ConditionVariables(t *testing.T) {
	job := func(extra string) string {
		return strings.Replace(round14Base, "        steps:", extra+"        steps:", 1)
	}
	top := func(vars string) string {
		return strings.Replace(round14Base, "stages:", "variables: {"+vars+"}\nstages:", 1)
	}
	for _, c := range []struct{ src, path string }{
		{top("BRANCH_NAME: main"), "variables.BRANCH_NAME"},
		{job("        env: {TAG_NAME: v1}\n"), "stages[0].jobs[0].env.TAG_NAME"},
		{job("        env: {CHANGE_ID: \"1\"}\n"), "stages[0].jobs[0].env.CHANGE_ID"},
	} {
		if d := round14(t, "jenkins", c.src); !hasDiag(d, diagnostics.Error, c.path, "conditions read") {
			t.Errorf("jenkins %s: %v", c.path, d)
		}
	}
	for _, c := range []struct{ src, path, msg string }{
		{top("CI_COMMIT_BRANCH: main"), "variables.CI_COMMIT_BRANCH", "generated rules read"},
		{job("        env: {CI_PIPELINE_SOURCE: push}\n"), "stages[0].jobs[0].env.CI_PIPELINE_SOURCE", "generated rules read"},
		{top("GIT_STRATEGY: none"), "variables.GIT_STRATEGY", "checkout steps"},
		{top("GIT_CHECKOUT: \"false\""), "variables.GIT_CHECKOUT", "checkout steps"},
	} {
		if d := round14(t, "gitlab", c.src); !hasDiag(d, diagnostics.Error, c.path, c.msg) {
			t.Errorf("gitlab %s: %v", c.path, d)
		}
	}
	if d := round14(t, "gitlab", top("GIT_DEPTH: \"1\"")); !hasDiag(d, diagnostics.Warning, "variables.GIT_DEPTH", "clones") {
		t.Errorf("gitlab GIT_DEPTH: %v", d)
	}
}

func TestRound23ArtifactUploads(t *testing.T) {
	src := strings.Replace(round14Base, "        steps: [{run: echo MARKER}]\n",
		"        steps:\n          - uploadArtifact: {name: dup, paths: [a]}\n          - uses: actions/upload-artifact@v4\n            with: {name: Dup, path: b}\n", 1)
	if d := round14(t, "github-actions", src); !hasDiag(d, diagnostics.Error, "stages[0].jobs[0].steps[1].with.name", "also uploaded") {
		t.Errorf("github duplicate raw upload: %v", d)
	}
	up := strings.Replace(round14Base, "        steps: [{run: echo MARKER}]\n", "        steps: [{uploadArtifact: {name: out, paths: [.next/]}}]\n", 1)
	if out := render19(t, "github-actions", up); !strings.Contains(out, "include-hidden-files: true") {
		t.Errorf("github upload skips hidden files:\n%s", out)
	}
	if out := render19(t, "jenkins", up); !strings.Contains(out, "defaultExcludes: false") || !strings.Contains(out, "useDefaultExcludes: false") {
		t.Errorf("jenkins upload drops default excludes:\n%s", out)
	}
}

func TestRound24GitVariables(t *testing.T) {
	top := func(vars string) string {
		return strings.Replace(round14Base, "stages:", "variables: {"+vars+"}\nstages:", 1)
	}
	step := func(env string) string {
		return strings.Replace(round14Base, "[{run: echo MARKER}]", "[{run: echo MARKER, env: {"+env+": x}}]", 1)
	}
	for _, c := range []struct{ prov, src, path string }{
		{"github-actions", top("GITHUB_SHA: x"), "variables.GITHUB_SHA"},
		{"azure-devops", top("build_sourceversion: x"), "variables.build_sourceversion"},
		{"jenkins", top("GIT_COMMIT: x"), "variables.GIT_COMMIT"},
		{"jenkins", step("GIT_COMMIT"), "stages[0].jobs[0].steps[0].env.GIT_COMMIT"},
		{"gitlab", step("CI_COMMIT_SHA"), "stages[0].jobs[0].steps[0].env.CI_COMMIT_SHA"},
		{"gitlab", top("CI_COMMIT_REF_NAME: x"), "variables.CI_COMMIT_REF_NAME"},
		{"bamboo", top("planRepository_branchName: x"), "variables.planRepository_branchName"},
		{"bamboo-java", step("planRepository_revision"), "stages[0].jobs[0].steps[0].env.planRepository_revision"},
	} {
		if d := round14(t, c.prov, c.src); !hasDiag(d, diagnostics.Error, c.path, "would replace the commit or branch") {
			t.Errorf("%s %s: %v", c.prov, c.path, d)
		}
	}
}

func TestRound24DefaultArtifactNames(t *testing.T) {
	for _, c := range []struct{ name, uses string }{
		{"artifact", "actions/upload-artifact@v4"},
		{"merged-artifacts", "actions/upload-artifact/merge@v4"},
	} {
		src := strings.Replace(round14Base, "        steps: [{run: echo MARKER}]\n",
			"        steps:\n          - uploadArtifact: {name: "+c.name+", paths: [a]}\n          - uses: "+c.uses+"\n", 1)
		if d := round14(t, "github-actions", src); !hasDiag(d, diagnostics.Error, "stages[0].jobs[0].steps[1].with.name", "also uploaded") {
			t.Errorf("%s default name: %v", c.uses, d)
		}
	}
}

func TestRound25BambooNames(t *testing.T) {
	sec := strings.Replace(round14Base, "stages:", "secrets: [API_TOKEN]\nstages:", 1)
	sec = strings.Replace(sec, "{run: echo MARKER}", "{run: \"echo ${secrets.API_TOKEN}\"}", 1)
	for _, prov := range []string{"bamboo", "bamboo-java"} {
		if d := round14(t, prov, sec); !hasDiag(d, diagnostics.Error, "secrets[0]", "plain text") {
			t.Errorf("%s secret without a secret word: %v", prov, d)
		}
		env := strings.Replace(round14Base, "[{run: echo MARKER}]", "[{run: echo MARKER, env: {bamboo_V: x}}]", 1)
		if d := round14(t, prov, env); !hasDiag(d, diagnostics.Error, "stages[0].jobs[0].steps[0].env.bamboo_V", "bamboo_") {
			t.Errorf("%s bamboo_ env: %v", prov, d)
		}
	}
}

func TestRound25JenkinsTimeoutAndPwsh(t *testing.T) {
	src := strings.Replace(round14Base, "        steps: [{run: echo MARKER}]", "        timeoutMinutes: 30\n        steps: [{run: echo MARKER}]", 1)
	out := render19(t, "jenkins", src)
	if steps, timeout := strings.Index(out, "steps {"), strings.Index(out, "timeout("); timeout < steps {
		t.Errorf("jenkins timeout is a stage option:\n%s", out)
	}
	if !strings.Contains(out, "timeout(time: 30, unit: 'MINUTES') {") {
		t.Errorf("jenkins timeout does not wrap the steps:\n%s", out)
	}
	win := strings.Replace(round14Base, "        steps: [{run: echo MARKER}]", "        runsOn: windows\n        steps: [{run: echo MARKER}]", 1)
	for _, prov := range []string{"github-actions", "azure-devops", "gitlab", "jenkins"} {
		if out := render19(t, prov, win); !strings.Contains(out, "PSNativeCommandUseErrorActionPreference = $true") {
			t.Errorf("%s pwsh script does not stop on failing commands:\n%s", prov, out)
		}
	}
}

func TestRound26JenkinsEmptyValues(t *testing.T) {
	src := strings.Replace(round14Base, "stages:", "variables: {V: \"\"}\nstages:", 1)
	if d := round14(t, "jenkins", src); !hasDiag(d, diagnostics.Warning, "variables.V", "unset instead of empty") {
		t.Errorf("jenkins empty variable: %v", d)
	}
}

func TestRound27GitLabSecretNames(t *testing.T) {
	for _, name := range []string{"CI_PIPELINE_SOURCE", "GIT_STRATEGY"} {
		src := strings.Replace(round14Base, "stages:", "secrets: ["+name+"]\nstages:", 1)
		if d := round14(t, "gitlab", src); !hasDiag(d, diagnostics.Error, "secrets[0]", "") {
			t.Errorf("gitlab secret %s: %v", name, d)
		}
	}
}

func TestRound28JavaPackageDeviceNames(t *testing.T) {
	src := strings.Replace(round14Base, "bamboo: {projectKey: PROJ, planKey: PLAN}", "bamboo: {projectKey: PROJ, planKey: PLAN, javaPackage: com.acme.Con.x}", 1)
	if d := round14(t, "bamboo-java", src); !hasDiag(d, diagnostics.Error, "extensions.bamboo.javaPackage", "reserved Windows device name") {
		t.Errorf("javaPackage device name: %v", d)
	}
}

func TestRound30GitLabExpansion(t *testing.T) {
	src := strings.Replace(round14Base, "stages:", "variables: {PCT: \"100%CI_JOB_TOKEN%x\"}\nstages:", 1)
	if d := round14(t, "gitlab", src); !hasDiag(d, diagnostics.Error, "variables.PCT", "would expand") {
		t.Errorf("gitlab %%VAR%%: %v", d)
	}
	sec := strings.Replace(round14Base, "stages:", "secrets: [CI_JOB_TOKEN]\nstages:", 1)
	if d := round14(t, "gitlab", sec); !hasDiag(d, diagnostics.Error, "secrets[0]", "predefined variables") {
		t.Errorf("gitlab CI_ secret: %v", d)
	}
}

func TestRound31ExpansionPositions(t *testing.T) {
	env := strings.Replace(round14Base, "        steps:", "        environment: {name: prod, url: \"https://x.example/%CI_JOB_ID%\"}\n        steps:", 1)
	if d := round14(t, "gitlab", env); !hasDiag(d, diagnostics.Error, "stages[0].jobs[0].environment.url", "would expand") {
		t.Errorf("gitlab environment url: %v", d)
	}
	macro := strings.Replace(round14Base, "stages:", "variables: {date: x}\nstages:", 1)
	macro = strings.Replace(macro, "{run: echo MARKER}", "{run: \"echo $(Date) $(echo MARKER)\"}", 1)
	if d := round14(t, "azure-devops", macro); !hasDiag(d, diagnostics.Error, "stages[0].jobs[0].steps[0].run", "Azure replaces $(Date)") {
		t.Errorf("azure script macro: %v", d)
	}
	plain := strings.Replace(round14Base, "{run: echo MARKER}", "{run: \"echo $(date)\"}", 1)
	if d := round14(t, "azure-devops", plain); hasDiag(d, diagnostics.Error, "stages[0].jobs[0].steps[0].run", "Azure replaces") {
		t.Errorf("azure plain command substitution rejected: %v", d)
	}
}
