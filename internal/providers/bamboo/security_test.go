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
