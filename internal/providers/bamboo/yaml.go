package bamboo

import (
	"context"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/amitpatole/renvoy/internal/diagnostics"
	"github.com/amitpatole/renvoy/internal/pipeline"
	"github.com/amitpatole/renvoy/internal/providers"
	"github.com/amitpatole/renvoy/internal/providers/shared"
	"github.com/amitpatole/renvoy/internal/render"
)

// YAMLName is the identifier of the YAML Specs provider.
const YAMLName = "bamboo"

// YAMLProvider renders Bamboo YAML Specs.
type YAMLProvider struct{}

// NewYAML returns the Bamboo YAML Specs provider.
func NewYAML() *YAMLProvider { return &YAMLProvider{} }

// Name implements providers.Provider.
func (*YAMLProvider) Name() string { return YAMLName }

// DisplayName implements providers.Provider.
func (*YAMLProvider) DisplayName() string { return "Bamboo YAML Specs" }

// Description implements providers.Provider.
func (*YAMLProvider) Description() string {
	return "Atlassian Bamboo YAML Specs (bamboo-specs/bamboo.yaml)"
}

// Capabilities implements providers.Provider.
func (*YAMLProvider) Capabilities() providers.CapabilitySet { return capabilities(false) }

// Validate implements providers.Provider.
func (pr *YAMLProvider) Validate(p *pipeline.Pipeline) diagnostics.List {
	return validate(p, pr, false)
}

// Render implements providers.Provider.
func (pr *YAMLProvider) Render(_ context.Context, p *pipeline.Pipeline, _ providers.RenderOptions) ([]providers.GeneratedFile, error) {
	ext, diags := decode(p, YAMLName)
	if diags.HasErrors() {
		return nil, fmt.Errorf("bamboo: invalid extensions")
	}
	pl := buildPlan(p, ext)
	names := map[string]string{}
	for _, j := range p.AllJobs() {
		names[j.Name] = yamlJobName(j)
	}

	root := shared.NewMap()
	root.Set("version", 2)
	pm := shared.NewMap().Set("project-key", pl.ProjectKey).Set("key", pl.PlanKey).Set("name", pl.Name)
	if pl.Description != "" {
		pm.Set("description", pl.Description)
	}
	root.Set("plan", pm)

	var stages []*shared.Map
	for _, s := range pl.Stages {
		var jobs []string
		for _, j := range s.Jobs {
			jobs = append(jobs, names[j.Name])
		}
		sm := shared.NewMap()
		if s.Manual {
			sm.Set("manual", true)
		}
		sm.Set("jobs", jobs)
		stages = append(stages, shared.NewMap().Set(s.Name, sm))
	}
	root.Set("stages", stages)

	if len(pl.Variables) > 0 {
		vars := shared.NewMap()
		for _, v := range pl.Variables {
			vars.Set(v.Name, v.Value)
		}
		root.Set("variables", vars)
	}
	// Without a triggers key Bamboo uses its default repository trigger; listing
	// any trigger replaces that default, so polling is then listed explicitly.
	if !pl.PollRepository || len(pl.Crons) > 0 {
		trig := shared.Seq()
		if pl.PollRepository {
			trig.Content = append(trig.Content, shared.NewMap().Set("polling", 180).Node())
		}
		for _, c := range pl.Crons {
			trig.Content = append(trig.Content, shared.NewMap().Set("cron", c).Node())
		}
		if len(trig.Content) == 0 {
			trig.Style = yaml.FlowStyle
		}
		root.Set("triggers", trig)
	}
	switch pl.BranchCreate {
	case "for-pull-request":
		root.Set("branches", shared.NewMap().Set("create", "for-pull-request"))
	case "for-new-branch":
		root.Set("branches", shared.NewMap().Set("create", shared.NewMap().Set("for-new-branch", pl.BranchRegex)))
	}

	for _, s := range pl.Stages {
		for _, j := range s.Jobs {
			root.Set(names[j.Name], yamlJob(j))
		}
	}
	body, err := shared.Encode(root)
	if err != nil {
		return nil, err
	}
	content := "---\n" + string(body) + "\n" + render.CustomBlock(providers.CommentHash, "")
	return []providers.GeneratedFile{{Path: "bamboo-specs/bamboo.yaml", Content: []byte(content),
		Comment: providers.CommentHash, YAML: true}}, nil
}

func conditions(branches []string) []*shared.Map {
	var out []*shared.Map
	for _, b := range branches {
		out = append(out, shared.NewMap().Set("variable",
			shared.NewMap().Set("equals", shared.NewMap().Set(branchVariable, b))))
	}
	return out
}

func yamlJob(j *job) *shared.Map {
	m := shared.NewMap().Set("key", j.Key)
	if j.Image != "" {
		d := shared.NewMap().Set("image", j.Image)
		if j.RunArgs != "" {
			d.Set("docker-run-arguments", []string{j.RunArgs})
		}
		m.Set("docker", d)
	}
	if len(j.Requirements) > 0 {
		seq := shared.Seq()
		for _, r := range j.Requirements {
			if r.Match == "" {
				seq.Content = append(seq.Content, shared.Str(r.Key))
			} else {
				seq.Content = append(seq.Content, shared.NewMap().Set(r.Key, r.Match).Node())
			}
		}
		m.Set("requirements", seq)
	}
	tasks := shared.Seq()
	for _, t := range j.Tasks {
		tasks.Content = append(tasks.Content, yamlTask(t))
	}
	m.Set("tasks", tasks)
	if len(j.FinalTasks) > 0 {
		final := shared.Seq()
		for _, t := range j.FinalTasks {
			final.Content = append(final.Content, yamlTask(t))
		}
		m.Set("final-tasks", final)
	}
	if len(j.Artifacts) > 0 {
		var arts []*shared.Map
		for _, a := range j.Artifacts {
			arts = append(arts, shared.NewMap().Set("name", a.Name).Set("location", a.Location).
				Set("pattern", a.Pattern).Set("shared", true).Set("required", true))
		}
		m.Set("artifacts", arts)
	}
	subs := shared.Seq()
	for _, s := range j.Subscriptions {
		subs.Content = append(subs.Content, shared.NewMap().Set("artifact", s.Name).Set("destination", s.Location).Node())
	}
	if len(subs.Content) == 0 {
		subs.Style = yaml.FlowStyle
	}
	m.Set("artifact-subscriptions", subs)
	return m
}

func yamlTask(t task) *yaml.Node {
	switch t.Kind {
	case taskCheckout:
		return shared.Str("checkout")
	case taskJUnit:
		tp := shared.NewMap().Set("type", "junit").Set("test-results", []string{t.Results})
		if len(t.Branches) > 0 {
			tp.Set("conditions", conditions(t.Branches))
		}
		return shared.NewMap().Set("test-parser", tp).Node()
	}
	interpreter := "SHELL"
	if t.PowerShell {
		interpreter = "WINDOWS_POWER_SHELL"
	}
	body := t.Body
	if t.Description != "" {
		if first, rest, ok := strings.Cut(body, "\n"); ok && strings.HasPrefix(first, "#!") {
			body = first + "\n# " + t.Description + "\n" + rest
		} else {
			body = "# " + t.Description + "\n" + body
		}
	}
	sm := shared.NewMap().Set("interpreter", interpreter)
	sm.Set("scripts", shared.Seq(shared.Str(shared.Block(body))))
	if t.Env != "" {
		sm.Set("environment", t.Env)
	}
	if t.WorkDir != "" {
		sm.Set("working-dir", t.WorkDir)
	}
	if len(t.Branches) > 0 {
		sm.Set("conditions", conditions(t.Branches))
	}
	return shared.NewMap().Set("script", sm).Node()
}
