package pipeline_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/amitpatole/renvoy/internal/diagnostics"
	"github.com/amitpatole/renvoy/internal/pipeline"
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
		"workingDirectory":           "          - run: x\n            workingDirectory: -C\n",
	}
	for field, step := range steps {
		src := strings.Replace(minimal, "          - run: make test\n", step, 1)
		_, diags := parse(t, src)
		expectError(t, diags, "stages[0].jobs[0].steps[0]."+field, "must not start with '-'")
	}
	src := strings.Replace(minimal, "          - run: make test\n", "          - pushImage:\n              image: --all-tags\n", 1)
	_, diags := parse(t, src)
	expectError(t, diags, "stages[0].jobs[0].steps[0].pushImage.image", "not a valid image reference")
}

// Rendering inlines variable values, so input size alone does not bound output.
func TestAmplificationBudgets(t *testing.T) {
	big := strings.Repeat("a", pipeline.MaxVariableValue+1)
	_, diags := parse(t, minimal+"variables:\n  BIG: "+big+"\n")
	expectError(t, diags, "variables.BIG", "at most 4096")

	val := strings.Repeat("b", 4000)
	refs := strings.Repeat("${variables.V}", 300)
	src := strings.Replace(minimal, "          - run: make test\n", "          - run: echo "+refs+"\n", 1) + "variables:\n  V: " + val + "\n"
	_, diags = parse(t, src)
	expectError(t, diags, "variables", "variable references expand to")

	var b strings.Builder
	b.WriteString(strings.Split(minimal, "stages:")[0] + "stages:\n  - name: test\n    jobs:\n")
	for i := 0; i < 6; i++ {
		b.WriteString("      - name: m" + string(rune('a'+i)) + "\n        matrix:\n          x: [" + vals(256) + "]\n        steps:\n          - run: x\n")
	}
	_, diags = parse(t, b.String())
	expectError(t, diags, "stages", "combinations in total")
}

func vals(n int) string {
	out := make([]string, n)
	for i := range out {
		out[i] = "v" + strings.Repeat("0", 3-len(itoa(i))) + itoa(i)
	}
	return strings.Join(out, ", ")
}

func itoa(i int) string { return strconv.Itoa(i) }

// Provider-native substitution syntax hides untrusted data in script text.
func TestProviderNativeInterpolationWarns(t *testing.T) {
	src := strings.Replace(minimal, "          - run: make test\n",
		"          - run: echo \"$(Build.SourceBranchName)\" $${bamboo.planRepository.branchName} $(date)\n", 1)
	_, diags := parse(t, src)
	if !hasWarning(diags, "stages[0].jobs[0].steps[0].run", "provider-specific substitution syntax") {
		t.Fatalf("expected a warning:\n%s", render(diags))
	}
	plain := strings.Replace(minimal, "          - run: make test\n", "          - run: echo \"$(date)\" \"$(git rev-parse HEAD)\"\n", 1)
	_, diags = parse(t, plain)
	if hasWarning(diags, "stages[0].jobs[0].steps[0].run", "provider-specific substitution syntax") {
		t.Fatalf("plain shell command substitution must not warn:\n%s", render(diags))
	}
}

// Any variable that selects programs, configuration, or runner behavior must not
// take its value from a branch name.
func TestSensitiveNamesRejectBranchValues(t *testing.T) {
	for _, name := range []string{"GIT_SSH", "GIT_PROXY_COMMAND", "KUBECONFIG", "HOME", "XDG_CONFIG_HOME",
		"DOCKER_CONFIG", "CDPATH", "GLIBC_TUNABLES", "CI_DEBUG_TRACE", "FF_USE_FASTZIP", "MAVEN_OPTS", "npm_config_userconfig"} {
		src := strings.Replace(minimal, "        steps:\n", "        env:\n          "+name+": '${git.branch}'\n        steps:\n", 1)
		_, diags := parse(t, src)
		expectError(t, diags, "stages[0].jobs[0].env."+name, "cannot be set from ${git.branch}")
	}
	ok := strings.Replace(minimal, "        steps:\n", "        env:\n          BRANCH_NAME: '${git.branch}'\n        steps:\n", 1)
	if _, diags := parse(t, ok); diags.HasErrors() {
		t.Fatalf("a neutral name must accept ${git.branch}:\n%s", render(diags))
	}
}

func TestDuplicateKeysInOrderedMaps(t *testing.T) {
	src := strings.Replace(minimal, "        steps:\n", "        env:\n          MODE: a\n          MODE: b\n        steps:\n", 1)
	if p, _, diags := pipeline.Parse([]byte(src), "renvoy.yaml"); p != nil || !strings.Contains(render(diags), "defined more than once") {
		t.Fatalf("duplicate env keys must be rejected:\n%s", render(diags))
	}
}

func TestContainerAndMatrixImagesAreChecked(t *testing.T) {
	for name, src := range map[string]string{
		"leading dash": strings.Replace(minimal, "      - name: unit\n", "      - name: unit\n        container:\n          image: \"-v/:/host alpine\"\n", 1),
		"via variable": strings.Replace(minimal, "      - name: unit\n", "      - name: unit\n        container:\n          image: ${variables.IMG}\n", 1) + "variables:\n  IMG: \"-v/:/host alpine\"\n",
	} {
		_, diags := parse(t, src)
		if !strings.Contains(render(diags), "not a valid image reference") {
			t.Errorf("%s: expected an image error:\n%s", name, render(diags))
		}
	}
	src := strings.Replace(minimal, "      - name: unit\n", "      - name: unit\n        matrix:\n          img: [\"-v\"]\n", 1)
	_, diags := parse(t, src)
	expectError(t, diags, "stages[0].jobs[0].matrix.img[0]", "matrix value")
}

// Round 3: more names that pick programs, flags, or credential files.
func TestMoreSensitiveNamesRejectBranchValues(t *testing.T) {
	for _, name := range []string{"MAKEFLAGS", "RUSTC_WRAPPER", "UV_CONFIG_FILE", "AWS_CONFIG_FILE",
		"GOOGLE_APPLICATION_CREDENTIALS", "BUNDLE_GEMFILE", "TF_CLI_ARGS", "CC", "CXX"} {
		src := strings.Replace(minimal, "        steps:\n", "        env:\n          "+name+": '${git.branch}'\n        steps:\n", 1)
		_, diags := parse(t, src)
		expectError(t, diags, "stages[0].jobs[0].env."+name, "cannot be set from ${git.branch}")
	}
}

func TestPatternLimits(t *testing.T) {
	long := strings.Repeat("a", pipeline.MaxPatternLength+1)
	_, diags := parse(t, strings.Replace(minimal, "push: {}", "push:\n    branches: ["+long+"]", 1))
	expectError(t, diags, "triggers.push.branches[0]", "at most 255")
	many := strings.TrimSuffix(strings.Repeat("b,", pipeline.MaxPatterns+1), ",")
	_, diags = parse(t, strings.Replace(minimal, "push: {}", "push:\n    branches: ["+many+"]", 1))
	expectError(t, diags, "triggers.push.branches", "at most 100")
}

// Round 5: branch values are allowed only in names that describe the value.
func TestBranchValuesUseAnAllowlist(t *testing.T) {
	for _, name := range []string{"LUA_INIT", "DOTNET_STARTUP_HOOKS", "VIMINIT", "PHPRC", "OPENSSL_CONF",
		"COMSPEC", "ZDOTDIR", "LESSOPEN", "GREETING"} {
		src := strings.Replace(minimal, "        steps:\n", "        env:\n          "+name+": 'x-${git.branch}'\n        steps:\n", 1)
		_, diags := parse(t, src)
		expectError(t, diags, "stages[0].jobs[0].env."+name, "cannot be set from ${git.branch}")
	}
	for _, name := range []string{"BRANCH_NAME", "IMAGE_TAG", "SOURCE_REF", "APP_VERSION", "image_label"} {
		src := strings.Replace(minimal, "        steps:\n", "        env:\n          "+name+": 'x-${git.branch}'\n        steps:\n", 1)
		if _, diags := parse(t, src); diags.HasErrors() {
			t.Errorf("%s should accept a branch value:\n%s", name, render(diags))
		}
	}
}

// Round 5: Azure writes every axis into every combination, so axis text is budgeted.
func TestMatrixAxisLimits(t *testing.T) {
	var axes strings.Builder
	for i := 0; i < pipeline.MaxMatrixAxes+1; i++ {
		axes.WriteString("          a" + strconv.Itoa(i) + ": [x]\n")
	}
	_, diags := parse(t, strings.Replace(minimal, "        steps:\n", "        matrix:\n"+axes.String()+"        steps:\n", 1))
	expectError(t, diags, "stages[0].jobs[0].matrix", "at most 16")

	var b strings.Builder
	b.WriteString(strings.Split(minimal, "stages:")[0] + "stages:\n  - name: test\n    jobs:\n")
	long := strings.Repeat("v", 60)
	for j := 0; j < 4; j++ {
		b.WriteString("      - name: m" + strconv.Itoa(j) + "\n        matrix:\n")
		for i := 0; i < 8; i++ {
			b.WriteString("          b" + strconv.Itoa(i) + strings.Repeat("x", 60) + ": [p, q]\n")
		}
		for i := 0; i < 8; i++ {
			b.WriteString("          c" + strconv.Itoa(i) + strings.Repeat("x", 60) + ": [" + long + "]\n")
		}
		b.WriteString("        steps:\n          - run: x\n")
	}
	_, diags = parse(t, b.String())
	expectError(t, diags, "stages", "bytes of matrix entries")
}

// Round 6: a matrix far over the combination limit must fail fast, not loop once
// per combination (5 KB of input previously hung validation).
func TestHugeMatrixFailsFast(t *testing.T) {
	var vals []string
	for i := 0; i < 100; i++ {
		vals = append(vals, "v"+strconv.Itoa(i))
	}
	var axes strings.Builder
	for i := 0; i < 5; i++ {
		axes.WriteString("          a" + strconv.Itoa(i) + ": [" + strings.Join(vals, ", ") + "]\n")
	}
	src := strings.Replace(minimal, "      - name: unit\n",
		"      - name: unit\n        container:\n          image: python:${matrix.a0}\n        matrix:\n"+axes.String(), 1)
	done := make(chan diagnostics.List, 1)
	go func() {
		_, diags := parse(t, src)
		done <- diags
	}()
	select {
	case diags := <-done:
		expectError(t, diags, "stages[0].jobs[0].matrix", "more than 256")
	case <-time.After(5 * time.Second):
		t.Fatal("validation did not finish")
	}
}

// Round 6: Docker applies predefined args such as HTTP_PROXY to every RUN, so
// build arguments follow the same branch-name rules as env.
func TestBuildArgsFollowBranchRules(t *testing.T) {
	for _, name := range []string{"HTTP_PROXY", "https_proxy", "NODE_OPTIONS", "PIP_INDEX_URL"} {
		src := strings.Replace(minimal, "          - run: make test\n",
			"          - buildImage:\n              image: app:1\n              buildArgs:\n                "+name+": '${git.branch}'\n", 1)
		_, diags := parse(t, src)
		expectError(t, diags, "stages[0].jobs[0].steps[0].buildImage.buildArgs."+name, "cannot be set from ${git.branch}")
	}
	ok := strings.Replace(minimal, "          - run: make test\n",
		"          - buildImage:\n              image: app:1\n              buildArgs:\n                GIT_BRANCH_NAME: x\n                SOURCE_BRANCH: '${git.branch}'\n", 1)
	if _, diags := parse(t, ok); diags.HasErrors() {
		t.Fatalf("neutral build arguments must be accepted:\n%s", render(diags))
	}
}

// Round 6: the allowlist matches whole words, not substrings.
func TestBranchAllowlistMatchesWholeWords(t *testing.T) {
	for _, name := range []string{"HOSTNAME", "STAGE", "PREFIX", "LOGNAME", "PIPENV_CUSTOM_VENV_NAME"} {
		src := strings.Replace(minimal, "        steps:\n", "        env:\n          "+name+": '${git.branch}'\n        steps:\n", 1)
		_, diags := parse(t, src)
		expectError(t, diags, "stages[0].jobs[0].env."+name, "cannot be set from ${git.branch}")
	}
	for _, name := range []string{"DEPLOY_ENV", "APP_ENVIRONMENT", "RELEASE_CHANNEL", "DEPLOY_TARGET", "PREVIEW_URL", "BRANCH_SLUG"} {
		src := strings.Replace(minimal, "        steps:\n", "        env:\n          "+name+": '${git.branch}'\n        steps:\n", 1)
		if _, diags := parse(t, src); diags.HasErrors() {
			t.Errorf("%s should accept a branch value:\n%s", name, render(diags))
		}
	}
}

// Round 7: cron validation was quadratic in the field length.
func TestLongCronFailsFast(t *testing.T) {
	cron := "0 3 * * " + strings.TrimSuffix(strings.Repeat("1,", 200000), ",")
	start := time.Now()
	if err := pipeline.ValidateCron(cron); err == nil || !strings.Contains(err.Error(), "at most 256") {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("cron validation took %v", d)
	}
}

// Round 7: custom-block marker text in rendered values is refused at validation.
func TestMarkerTextInValuesIsRefused(t *testing.T) {
	src := strings.Replace(minimal, "          - run: make test\n", "          - run: |\n              # renvoy:begin custom\n              make test\n", 1)
	_, diags := parse(t, src)
	expectError(t, diags, "stages[0].jobs[0].steps[0].run", "marker text")
	ok := strings.Replace(minimal, "          - run: make test\n", "          - name: \"Renvoy: end-to-end tests\"\n            run: make e2e\n", 1)
	if _, diags := parse(t, ok); diags.HasErrors() {
		t.Fatalf("an ordinary step name was refused:\n%s", render(diags))
	}
}

// Round 7: names that steer environments, hosts, or tool versions are not neutral.
func TestMoreToolNamesRejectBranchValues(t *testing.T) {
	for _, name := range []string{"VIRTUAL_ENV", "COMPOSE_ENV_FILES", "SONAR_HOST_URL", "REGISTRY_URL",
		"PYENV_VERSION", "RBENV_VERSION", "BUILDX_BUILDER_NAME", "CACHE_DIRS", "API_HOST"} {
		src := strings.Replace(minimal, "        steps:\n", "        env:\n          "+name+": 'x-${git.branch}'\n        steps:\n", 1)
		_, diags := parse(t, src)
		expectError(t, diags, "stages[0].jobs[0].env."+name, "cannot be set from ${git.branch}")
	}
}

// Round 8: every string field is checked for marker text, not a chosen few.
func TestMarkerTextInAnyField(t *testing.T) {
	src := strings.Replace(minimal, "          - run: make test\n",
		"          - buildImage:\n              image: app:1\n              buildArgs:\n                NOTE: \"x\\n# renvoy:begin custom\\ny\"\n", 1)
	_, diags := parse(t, src)
	expectError(t, diags, "stages[0].jobs[0].steps[0].buildImage.buildArgs.NOTE", "marker text")
}

// Round 8: cron items must be well-formed, in range, and not reversed.
func TestCronItems(t *testing.T) {
	for _, bad := range []string{"*/0 * * * *", "5-1 0 * * *", "1-2-3 * * * *", "*/5/5 * * * *", "0 0 * * ,", "*-* * * * *", "60 * * * *", "0 0 0 * *"} {
		if err := pipeline.ValidateCron(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	for _, good := range []string{"0 3 * * 1-5", "*/15 * * * *", "0 0 1,15 * *", "5-10/2 * * * *", "0 0 * * 0"} {
		if err := pipeline.ValidateCron(good); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
}

// Round 9: a step equal to the field width is valid.
func TestCronStepUpToFieldWidth(t *testing.T) {
	for _, good := range []string{"0 */24 * * *", "*/60 * * * *"} {
		if err := pipeline.ValidateCron(good); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
	if err := pipeline.ValidateCron("0 */25 * * *"); err == nil {
		t.Error("a step wider than the field was accepted")
	}
}

// Round 11: metadata.labels was decoded by the YAML library, whose duplicate-key
// check is quadratic (70k labels took 38 s). Labels are now ordered and bounded,
// and every plain map is bounded before the library decodes it.
func TestLabelsAndMapsAreBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("metadata:\n  name: demo\n  labels:\n")
	for i := 0; i < 70000; i++ {
		b.WriteString("    l" + strconv.Itoa(i) + ": v\n")
	}
	src := strings.Replace(minimal, "metadata:\n  name: demo\n", b.String(), 1)
	start := time.Now()
	_, diags := parse(t, src)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("validating 70k labels took %v", d)
	}
	expectError(t, diags, "metadata.labels", "at most 100")

	dup := strings.Replace(minimal, "metadata:\n  name: demo\n", "metadata:\n  name: demo\n  labels:\n    team: a\n    team: b\n", 1)
	if p, _, diags := pipeline.Parse([]byte(dup), "renvoy.yaml"); p != nil || !strings.Contains(render(diags), "defined more than once") {
		t.Fatalf("duplicate labels must be rejected:\n%s", render(diags))
	}

	var perms strings.Builder
	perms.WriteString("extensions:\n  github-actions:\n    permissions:\n")
	for i := 0; i < 50000; i++ {
		perms.WriteString("      p" + strconv.Itoa(i) + ": read\n")
	}
	ext := minimal + perms.String()
	node := extensionNode(t, ext)
	start = time.Now()
	var target struct {
		Permissions map[string]string `yaml:"permissions"`
	}
	diags = pipeline.DecodeStrict(node, &target, "extensions.github-actions", nil)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("decoding a 50k-entry extension map took %v", d)
	}
	if !strings.Contains(render(diags), "at most 256") {
		t.Fatalf("expected the map limit:\n%s", render(diags))
	}
}

func extensionNode(t *testing.T, src string) *yaml.Node {
	t.Helper()
	p, _, diags := pipeline.Parse([]byte(src), "renvoy.yaml")
	if p == nil {
		t.Fatalf("does not parse: %s", render(diags))
	}
	n := p.Extensions.Get("github-actions")
	if n == nil {
		t.Fatal("no extension node")
	}
	return n
}

// Round 12: schedule branch lists and schedules are bounded like other lists.
func TestScheduleLimits(t *testing.T) {
	var branches []string
	for i := 0; i < pipeline.MaxPatterns+1; i++ {
		branches = append(branches, "b"+strconv.Itoa(i))
	}
	src := strings.Replace(minimal, "push: {}", "schedules:\n    - cron: \"0 3 * * *\"\n      branches: ["+strings.Join(branches, ", ")+"]", 1)
	_, diags := parse(t, src)
	expectError(t, diags, "triggers.schedules[0].branches", "at most 100")

	var scheds strings.Builder
	scheds.WriteString("schedules:\n")
	for i := 0; i < pipeline.MaxSchedules+1; i++ {
		scheds.WriteString("    - cron: \"0 3 * * *\"\n      branches: [main]\n")
	}
	_, diags = parse(t, strings.Replace(minimal, "push: {}", strings.TrimSuffix(scheds.String(), "\n"), 1))
	expectError(t, diags, "triggers.schedules", "at most 20")
}

// Round 12: ordered maps (env, with, variables, build args) are bounded, because
// providers render them as mappings again.
func TestOrderedMapsAreBounded(t *testing.T) {
	var env strings.Builder
	env.WriteString("        env:\n")
	for i := 0; i < pipeline.MaxVarsEntries+1; i++ {
		env.WriteString("          E" + strconv.Itoa(i) + ": v\n")
	}
	src := strings.Replace(minimal, "        steps:\n", env.String()+"        steps:\n", 1)
	if p, _, diags := pipeline.Parse([]byte(src), "renvoy.yaml"); p != nil || !strings.Contains(render(diags), "at most 1000") {
		t.Fatalf("expected the entry limit:\n%s", render(diags))
	}
}

// Round 14: names that collide ignoring case, image references outside the
// Docker grammar, and Windows device names are rejected.
func TestRound14PortableNames(t *testing.T) {
	cases := []struct{ old, new, path, msg string }{
		{"        steps:\n", "        env: {Foo: a, FOO: b}\n        steps:\n", "stages[0].jobs[0].env.FOO", "only by case"},
		{"        steps:\n", "        matrix: {ax: [a], AX: [b]}\n        steps:\n", "stages[0].jobs[0].matrix.AX", "only by case"},
		{"  name: demo", "  name: nul", "metadata.name", "reserved Windows device name"},
	}
	for _, c := range cases {
		_, diags := parse(t, strings.Replace(minimal, c.old, c.new, 1))
		expectError(t, diags, c.path, c.msg)
	}
	for _, image := range []string{"Foo:1", "a//b", "a:", "a@", "a:b:c", "app:-1", "UPPER/Path"} {
		src := strings.Replace(minimal, "        steps:\n", "        container: {image: \""+image+"\"}\n        steps:\n", 1)
		_, diags := parse(t, src)
		expectError(t, diags, "stages[0].jobs[0].container.image", "not a valid image reference")
	}
	for _, image := range []string{"python:3.12-slim", "ghcr.io/acme/app:1.2", "localhost:5000/a/b", "Registry.Example.com/team/app",
		"mcr.microsoft.com/dotnet/sdk:8.0", "alpine@sha256:" + strings.Repeat("ab", 32), "a__b/c-d.e:Tag_1"} {
		src := strings.Replace(minimal, "        steps:\n", "        container: {image: \""+image+"\"}\n        steps:\n", 1)
		if _, diags := parse(t, src); diags.HasErrors() {
			t.Errorf("%s rejected:\n%s", image, render(diags))
		}
	}
}

// Round 15: artifact and cache paths are literal on every provider, and image
// digests must have the exact length of their algorithm.
func TestRound15PathsAndDigests(t *testing.T) {
	for _, p := range []string{"!dist/", "#notes", `build\out`, " dist/", "dist/ "} {
		src := strings.Replace(minimal, "          - run: make test\n", "          - uploadArtifact: {name: out, paths: [\""+strings.ReplaceAll(p, `\`, `\\`)+"\"]}\n", 1)
		_, diags := parse(t, src)
		expectError(t, diags, "stages[0].jobs[0].steps[0].uploadArtifact.paths[0]", "must not start with '!' or '#'")
	}
	image := func(ref string) diagnostics.List {
		_, diags := parse(t, strings.Replace(minimal, "        steps:\n", "        container: {image: \""+ref+"\"}\n        steps:\n", 1))
		return diags
	}
	for _, ref := range []string{"a@sha256:" + strings.Repeat("a", 63), "a@sha256:" + strings.Repeat("a", 65),
		"a@sha256:" + strings.Repeat("A", 64), "a@md5:" + strings.Repeat("a", 32)} {
		expectError(t, image(ref), "stages[0].jobs[0].container.image", "not a valid image reference")
	}
	for _, ref := range []string{"a@sha512:" + strings.Repeat("0", 128), "[::1]:5000/team/app:1"} {
		if diags := image(ref); diags.HasErrors() {
			t.Errorf("%s rejected:\n%s", ref, render(diags))
		}
	}
}

// Round 16: paths passed to file matchers have no '.' or empty segments, which
// Jenkins' Ant patterns match against nothing.
func TestRound16PatternPaths(t *testing.T) {
	for _, p := range []string{"./dist/", "./", ".", "dist/./x", "dist/.", "a//b"} {
		src := strings.Replace(minimal, "          - run: make test\n", "          - uploadArtifact: {name: out, paths: [\""+p+"\"]}\n", 1)
		_, diags := parse(t, src)
		expectError(t, diags, "stages[0].jobs[0].steps[0].uploadArtifact.paths[0]", "'.' or empty segments")
	}
	src := strings.Replace(minimal, "          - run: make test\n", "          - publishTestResults: {path: \"./reports/*.xml\", format: junit}\n", 1)
	_, diags := parse(t, src)
	expectError(t, diags, "stages[0].jobs[0].steps[0].publishTestResults.path", "'.' or empty segments")
	ok := strings.Replace(minimal, "          - run: make test\n", "          - buildImage: {image: app, context: .}\n          - uploadArtifact: {name: out, paths: [dist/, a.b/c]}\n", 1)
	if _, diags := parse(t, ok); diags.HasErrors() {
		t.Fatalf("rejected:\n%s", render(diags))
	}
}

// Round 17: blank container options, control and format characters in paths,
// and path segments Windows would rename are rejected.
func TestRound17PathsAndOptions(t *testing.T) {
	src := strings.Replace(minimal, "        steps:\n", "        container: {image: alpine, options: \"  \"}\n        steps:\n", 1)
	_, diags := parse(t, src)
	expectError(t, diags, "stages[0].jobs[0].container.options", "must not be blank")
	for _, p := range []string{"a\\u0000b/", "a\\u001bb/", "a\\u202eb/", "a\\u2028b/", "a/. /x", "dist./", "a/b "} {
		src := strings.Replace(minimal, "          - run: make test\n", "          - uploadArtifact: {name: out, paths: [\""+p+"\"]}\n", 1)
		if _, diags := parse(t, src); !diags.HasErrors() {
			t.Errorf("path %q accepted", p)
		}
	}
	for expr, want := range map[string][]int{"*/20 * * * *": {0, 20, 40}, "5/30 * * * *": {5, 35}, "1,2-3 * * * *": {1, 2, 3}, "0 3 * * *": {0}} {
		var got []int
		for m, on := range pipeline.CronMinutes(expr) {
			if on {
				got = append(got, m)
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", expr, got, want)
		}
	}
}

// Round 18: container options with control characters (Java counts U+001C to
// U+001F as whitespace, so Bamboo saw them as blank), and a hint for cron names.
func TestRound18OptionsAndCronNames(t *testing.T) {
	for _, opt := range []string{"\\u001f", "\\u001c \\u001d", "--user root\\u001e"} {
		src := strings.Replace(minimal, "        steps:\n", "        container: {image: alpine, options: \""+opt+"\"}\n        steps:\n", 1)
		_, diags := parse(t, src)
		expectError(t, diags, "stages[0].jobs[0].container.options", "control characters")
	}
	src := strings.Replace(minimal, "push: {}", "schedules: [{cron: \"0 9 * * MON-FRI\", branches: [main]}]", 1)
	_, diags := parse(t, src)
	expectError(t, diags, "triggers.schedules[0].cron", "use numbers instead of names")
}

func TestRound19TimeoutAndCronHint(t *testing.T) {
	src := strings.Replace(minimal, "        steps:\n", "        timeoutMinutes: 525601\n        steps:\n", 1)
	_, diags := parse(t, src)
	expectError(t, diags, "stages[0].jobs[0].timeoutMinutes", "at most 525600")
	src = strings.Replace(minimal, "        steps:\n", "        timeoutMinutes: 525600\n        steps:\n", 1)
	if _, diags := parse(t, src); diags.HasErrors() {
		t.Fatalf("one year should be accepted: %v", diags)
	}
	src = strings.Replace(minimal, "push: {}", "schedules: [{cron: \"0 9 L * *\", branches: [main]}]", 1)
	_, diags = parse(t, src)
	expectError(t, diags, "triggers.schedules[0].cron", "may contain only digits")
	for _, d := range diags {
		if strings.Contains(d.Message, "instead of names") {
			t.Fatalf("name hint for L: %v", d)
		}
	}
}

func TestRound21CronWhitespace(t *testing.T) {
	for _, cron := range []string{`0 3 * * 1`, `0\t3 * * *`, `0 3\n* * *`, `0 3 * * 1 `, `0\u000b3 * * *`} {
		src := strings.Replace(minimal, "push: {}", "schedules: [{cron: \""+cron+"\", branches: [main]}]", 1)
		_, diags := parse(t, src)
		expectError(t, diags, "triggers.schedules[0].cron", "separate its fields with spaces")
	}
}

func TestRound22CronSingleSpaces(t *testing.T) {
	for _, cron := range []string{" 0 3 * * *", "0 3 * * * ", "0  3 * * *"} {
		src := strings.Replace(minimal, "push: {}", "schedules: [{cron: \""+cron+"\", branches: [main]}]", 1)
		_, diags := parse(t, src)
		expectError(t, diags, "triggers.schedules[0].cron", "single spaces")
	}
}

func TestRound22DeadConditions(t *testing.T) {
	warns := func(src, path, msg string) bool {
		_, diags := parse(t, src)
		for _, d := range diags {
			if d.Severity == diagnostics.Warning && d.Path == path && strings.Contains(d.Message, msg) {
				return true
			}
		}
		return false
	}
	job := func(when string) string {
		return strings.Replace(minimal, "        steps:", "        when: "+when+"\n        steps:", 1)
	}
	for _, c := range []struct{ event, trigger string }{
		{"manual", "triggers.manual"}, {"schedule", "triggers.schedules"}, {"pullRequest", "triggers.pullRequest"},
	} {
		if !warns(job("{events: ["+c.event+"]}"), "stages[0].jobs[0].when.events[0]", "no "+c.trigger) {
			t.Errorf("event %s without its trigger is not reported", c.event)
		}
	}
	if warns(job("{events: [push]}"), "stages[0].jobs[0].when.events[0]", "never happens") {
		t.Error("push event with a push trigger is reported")
	}
	pr := strings.Replace(job("{branches: [main], events: [pullRequest]}"), "push: {}", "pullRequest: {}", 1)
	if !warns(pr, "stages[0].jobs[0].when", "never matches") {
		t.Error("pull-request-only condition with branches is not reported")
	}
	mixed := strings.Replace(job("{branches: [main], events: [push, pullRequest]}"), "push: {}", "push: {}\n  pullRequest: {}", 1)
	if warns(mixed, "stages[0].jobs[0].when", "never matches") {
		t.Error("condition with a push event is reported as never matching")
	}
}

func TestRound25MatrixImageValues(t *testing.T) {
	src := strings.Replace(minimal, "        steps:\n          - run: make test\n",
		"        matrix: {os: [linux, Linux]}\n        steps:\n          - buildImage: {image: \"ghcr.io/acme/app-${matrix.os}:1\"}\n", 1)
	_, diags := parse(t, src)
	expectError(t, diags, "stages[0].jobs[0].steps[0].buildImage.image", `matrix value os="Linux"`)
	tag := strings.Replace(minimal, "          - run: make test\n", "          - buildImage: {image: \"ghcr.io/acme/app:${git.branch}\"}\n", 1)
	_, diags = parse(t, tag)
	found := false
	for _, d := range diags {
		found = found || (d.Severity == diagnostics.Warning && strings.Contains(d.Message, "branch names with /"))
	}
	if !found {
		t.Errorf("no warning for ${git.branch} in an image:\n%s", render(diags))
	}
}

func TestRound26SecretCaseAndPwshFirstStatements(t *testing.T) {
	src := strings.Replace(minimal, "stages:", "secrets: [API_SECRET, api_secret]\nstages:", 1)
	_, diags := parse(t, src)
	expectError(t, diags, "secrets[1]", "only by case")
	for _, run := range []string{"using namespace System.IO", "# c\n  Param ($x)\necho $x", "[CmdletBinding()]\nparam()", "echo a\nUSING module Foo"} {
		src := strings.Replace(minimal, "          - run: make test\n", "          - shell: pwsh\n            run: "+strconv.Quote(run)+"\n", 1)
		_, diags := parse(t, src)
		expectError(t, diags, "stages[0].jobs[0].steps[0].run", "must come first")
	}
	for _, run := range []string{"$param = 1", "#requires -Version 7\necho a"} {
		src := strings.Replace(minimal, "          - run: make test\n", "          - shell: pwsh\n            run: "+strconv.Quote(run)+"\n", 1)
		_, diags := parse(t, src)
		for _, d := range diags {
			if strings.Contains(d.Message, "must come first") {
				t.Errorf("%q rejected: %s", run, d.Message)
			}
		}
	}
}

func TestRound27PwshLeadingStatementBypasses(t *testing.T) {
	for _, run := range []string{
		"<#\n.SYNOPSIS x\n#>\nparam($x)",
		"<# c #> using namespace System",
		"[ CmdletBinding()]\nparam()",
		"[OutputType([int])]\nparam($x)",
		"# c\rparam($x)",
		"# c\rusing namespace System",
		"\u00a0using namespace System",
		"\vusing namespace System",
		"# c\u2028param($x)",
		"\ufeffparam($x)",
	} {
		src := strings.Replace(minimal, "          - run: make test\n", "          - shell: pwsh\n            run: "+strconv.Quote(run)+"\n", 1)
		_, diags := parse(t, src)
		expectError(t, diags, "stages[0].jobs[0].steps[0].run", "must come first")
	}
	for _, run := range []string{"[string]$x = 'a'\necho $x", "<# c #>\necho using", "Write-Output 'param(1)'"} {
		src := strings.Replace(minimal, "          - run: make test\n", "          - shell: pwsh\n            run: "+strconv.Quote(run)+"\n", 1)
		_, diags := parse(t, src)
		for _, d := range diags {
			if strings.Contains(d.Message, "must come first") {
				t.Errorf("%q rejected: %s", run, d.Message)
			}
		}
	}
}

func TestRound27EnvCaseAcrossLevels(t *testing.T) {
	src := strings.Replace(minimal, "        steps:\n          - run: make test\n",
		"        env: {FOO: job}\n        steps:\n          - run: make test\n            env: {foo: step}\n", 1)
	_, diags := parse(t, src)
	found := false
	for _, d := range diags {
		found = found || (d.Severity == diagnostics.Warning && strings.Contains(d.Message, "only by case"))
	}
	if !found {
		t.Errorf("no warning:\n%s", render(diags))
	}
}

func TestRound28PwshTokenizer(t *testing.T) {
	for _, run := range []string{
		"# note <#\nusing namespace System\n# #>\necho hi",
		"#<#\nparam($x)\n#>",
		"param\n($x)\necho hi",
		"param # c\n($x)",
		";param($x)",
		"<# a\n#> <# b #>\n[CmdletBinding()]\nparam()",
	} {
		src := strings.Replace(minimal, "          - run: make test\n", "          - shell: pwsh\n            run: "+strconv.Quote(run)+"\n", 1)
		_, diags := parse(t, src)
		expectError(t, diags, "stages[0].jobs[0].steps[0].run", "must come first")
	}
	for _, run := range []string{
		"echo \"a`\"b\"\necho ok",
		"echo a#b\necho ok",
		"function f {\n  param($x)\n  $x\n}\nf 1",
	} {
		src := strings.Replace(minimal, "          - run: make test\n", "          - shell: pwsh\n            run: "+strconv.Quote(run)+"\n", 1)
		_, diags := parse(t, src)
		for _, d := range diags {
			if strings.Contains(d.Message, "must come first") {
				t.Errorf("%q rejected: %s", run, d.Message)
			}
		}
	}
}

func TestRound28EmptyTriggerLists(t *testing.T) {
	for _, c := range []struct{ trigger, path string }{
		{"push: {branches: []}", "triggers.push.branches"},
		{"push: {tags: []}", "triggers.push.tags"},
		{"pullRequest: {branches: []}", "triggers.pullRequest.branches"},
	} {
		_, diags := parse(t, strings.Replace(minimal, "push: {}", c.trigger, 1))
		expectError(t, diags, c.path, "list is empty")
	}
}

func TestRound29PwshEscapes(t *testing.T) {
	for _, run := range []string{
		"param `\n($x)",
		"using `\nnamespace System.IO",
		"echo `'\nusing namespace System.IO",
		"echo `#; using namespace System.IO",
		"echo a#b; using namespace System.IO",
		"echo \"$('\"')\"\nusing namespace X",
		"{using namespace System.IO}",
		"[\nCmdletBinding()]\nparam()",
		"[CmdletBinding]param()",
		"$s = @'\nusing namespace System\n'@",
		"# using namespace System.IO",
		"Write-Host \"using namespace System.IO\"",
		"function f { using namespace System }",
		"Write-Host \"$('\"')\"\nusing namespace System.IO",
		"[CmdletBinding]\nparam()",
	} {
		src := strings.Replace(minimal, "          - run: make test\n", "          - shell: pwsh\n            run: "+strconv.Quote(run)+"\n", 1)
		_, diags := parse(t, src)
		expectError(t, diags, "stages[0].jobs[0].steps[0].run", "must come first")
	}
}

func TestRound29EmptyConditionListsAndControlSecrets(t *testing.T) {
	src := strings.Replace(minimal, "        steps:", "        when: {branches: [], events: [push]}\n        steps:", 1)
	_, diags := parse(t, src)
	expectError(t, diags, "stages[0].jobs[0].when.branches", "list is empty")
	src = strings.Replace(minimal, "stages:", "secrets: [BASH_ENV]\nstages:", 1)
	_, diags = parse(t, src)
	found := false
	for _, d := range diags {
		found = found || (d.Severity == diagnostics.Warning && d.Path == "secrets[0]" && strings.Contains(d.Message, "changes how shells"))
	}
	if !found {
		t.Errorf("no warning for secret BASH_ENV:\n%s", render(diags))
	}
}

func TestRound30PwshAndSchedules(t *testing.T) {
	for _, run := range []string{
		"using\u000bnamespace System.IO",
		"[CmdletBinding][Foo]param()",
		"[CmdletBinding]\n[OutputType([int])]\nparam()",
		"[CmdletBinding]#c\nparam()",
	} {
		src := strings.Replace(minimal, "          - run: make test\n", "          - shell: pwsh\n            run: "+strconv.Quote(run)+"\n", 1)
		_, diags := parse(t, src)
		expectError(t, diags, "stages[0].jobs[0].steps[0].run", "must come first")
	}
	for _, run := range []string{"[string]$x = 'a'\necho $x", "$a[0]#c\necho ok"} {
		src := strings.Replace(minimal, "          - run: make test\n", "          - shell: pwsh\n            run: "+strconv.Quote(run)+"\n", 1)
		_, diags := parse(t, src)
		for _, d := range diags {
			if strings.Contains(d.Message, "must come first") {
				t.Errorf("%q rejected: %s", run, d.Message)
			}
		}
	}
	_, diags := parse(t, strings.Replace(minimal, "push: {}", "push: {}\n  schedules: []", 1))
	expectError(t, diags, "triggers.schedules", "list is empty")
}

func TestRound32BuildArgProviderSyntax(t *testing.T) {
	for _, val := range []string{"${{ secrets.X }}", "$(TOK)", "$[variables.x]", "$${bamboo.deploy.password}"} {
		src := strings.Replace(minimal, "          - run: make test\n",
			"          - buildImage:\n              image: app:1\n              buildArgs: {A: "+strconv.Quote(val)+"}\n", 1)
		_, diags := parse(t, src)
		expectError(t, diags, "stages[0].jobs[0].steps[0].buildImage.buildArgs.A", "provider expression syntax")
	}
}
