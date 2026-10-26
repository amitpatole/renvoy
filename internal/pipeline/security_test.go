package pipeline_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

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
