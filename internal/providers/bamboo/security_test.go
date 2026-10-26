package bamboo_test

import (
	"strings"
	"testing"

	"github.com/amitpatole/renvoy/internal/config"
	"github.com/amitpatole/renvoy/internal/diagnostics"
	"github.com/amitpatole/renvoy/internal/providers"
	"github.com/amitpatole/renvoy/internal/providers/bamboo"
)

// A branch name is controlled by whoever pushes it and may contain quotes. Bamboo
// parses task environment variables from one K="v" string after substituting
// variables, so ${git.branch} must never reach that string.
func TestBranchNameNeverReachesTaskEnvironment(t *testing.T) {
	scopes := map[string]string{
		"job":  "        env:\n          TAG: build-${git.branch}\n        steps:\n          - run: echo hi\n",
		"step": "        steps:\n          - run: echo hi\n            env:\n              TAG: build-${git.branch}\n",
	}
	for scope, jobBody := range scopes {
		src := `apiVersion: renvoy.dev/v1alpha1
kind: Pipeline
metadata: {name: poc}
triggers: {pullRequest: {}}
extensions:
  bamboo: {projectKey: POC, planKey: BRANCH}
stages:
  - name: build
    jobs:
      - name: tag
` + jobBody
		res := config.Compile([]byte(src), "renvoy.yaml", []string{"bamboo"})
		if res.Pipeline == nil {
			t.Fatalf("%s: does not compile: %v", scope, res.Diagnostics)
		}
		for _, prov := range []providers.Provider{bamboo.NewYAML(), bamboo.NewJava()} {
			rejected := false
			for _, d := range prov.Validate(res.Pipeline) {
				if d.Severity == diagnostics.Error && strings.Contains(d.Message, "${git.branch}") {
					rejected = true
				}
			}
			if !rejected {
				t.Errorf("%s env on %s: ${git.branch} was not rejected", scope, prov.Name())
			}
		}
	}
}

// Plan variables can be overridden when a build is run and secrets are not
// visible to Renvoy, so neither may be substituted into a Bamboo env string.
func TestVariablesAndSecretsNeverReachTaskEnvironment(t *testing.T) {
	for _, value := range []string{"${variables.GREETING}", "${secrets.TOKEN}", "a\\b", `say "hi"`} {
		src := `apiVersion: renvoy.dev/v1alpha1
kind: Pipeline
metadata: {name: poc}
triggers: {push: {}}
variables: {GREETING: hello}
secrets: [TOKEN]
extensions:
  bamboo: {projectKey: POC, planKey: VARS}
stages:
  - name: build
    jobs:
      - name: tag
        env:
          MSG: '` + strings.ReplaceAll(value, "'", "''") + `'
        steps:
          - run: echo hi
`
		res := config.Compile([]byte(src), "renvoy.yaml", []string{"bamboo"})
		if res.Pipeline == nil {
			t.Fatalf("%s: does not compile: %v", value, res.Diagnostics)
		}
		for _, prov := range []providers.Provider{bamboo.NewYAML(), bamboo.NewJava()} {
			if !prov.Validate(res.Pipeline).HasErrors() {
				t.Errorf("%s on %s was accepted in a task environment value", value, prov.Name())
			}
		}
	}
}

// An escaped $${ in renvoy.yaml becomes a literal ${ that Bamboo would substitute.
func TestEscapedSubstitutionNeverReachesTaskEnvironment(t *testing.T) {
	src := `apiVersion: renvoy.dev/v1alpha1
kind: Pipeline
metadata: {name: poc}
triggers: {push: {}}
extensions:
  bamboo: {projectKey: POC, planKey: ESC}
stages:
  - name: build
    jobs:
      - name: tag
        env:
          GREETING: 'hi $${bamboo.planRepository.branchName}'
        steps:
          - run: echo hi
`
	res := config.Compile([]byte(src), "renvoy.yaml", []string{"bamboo"})
	if res.Pipeline == nil {
		t.Fatalf("does not compile: %v", res.Diagnostics)
	}
	if !bamboo.NewYAML().Validate(res.Pipeline).HasErrors() {
		t.Fatal("a literal ${ in a Bamboo env value was accepted")
	}
}
