package shared

import (
	"strings"

	"github.com/amitpatole/renvoy/internal/diagnostics"
	"github.com/amitpatole/renvoy/internal/pipeline"
)

// GitVarCollisions reports names the pipeline places in the environment
// (variables, secrets, job and step env, matrix axes) that equal, ignoring
// case, a variable a provider reads ${git.*} from. Such an entry would
// replace the commit or branch the scripts see.
func GitVarCollisions(p *pipeline.Pipeline, provider string, reads func(name string) bool) diagnostics.List {
	var diags diagnostics.List
	check := func(path, name string) {
		if reads(strings.ToUpper(name)) {
			diags = append(diags, diagnostics.Errorf(path, "%q is the variable %s reads ${git.sha} or ${git.branch} from, so this would replace the commit or branch", name, provider).
				WithHint("rename it").WithProvider(provider))
		}
	}
	for _, v := range p.Variables {
		check(pipeline.JoinKey("variables", v.Name), v.Name)
	}
	for i, name := range p.Secrets {
		check(pipeline.JoinIndex("secrets", i), name)
	}
	for _, j := range p.AllJobs() {
		for _, e := range j.Env {
			check(pipeline.JoinKey(j.Path()+".env", e.Name), e.Name)
		}
		for _, axis := range j.Matrix {
			check(pipeline.JoinKey(j.Path()+".matrix", axis.Name), axis.Name)
		}
		for _, s := range j.Steps {
			for _, e := range s.Env {
				check(pipeline.JoinKey(s.Path()+".env", e.Name), e.Name)
			}
		}
	}
	return diags
}
