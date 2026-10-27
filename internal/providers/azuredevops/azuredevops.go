// Package azuredevops renders pipelines as Azure DevOps YAML pipelines.
package azuredevops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/amitpatole/renvoy/internal/compiler"
	"github.com/amitpatole/renvoy/internal/diagnostics"
	"github.com/amitpatole/renvoy/internal/pipeline"
	"github.com/amitpatole/renvoy/internal/providers"
	"github.com/amitpatole/renvoy/internal/providers/shared"
	"github.com/amitpatole/renvoy/internal/render"
)

// Name is the provider identifier and extension namespace.
const Name = "azure-devops"

const (
	branchVar      = "RENVOY_GIT_BRANCH"
	containerVar   = "RENVOY_CONTAINER_IMAGE"
	branchRuntime  = "$[ replace(coalesce(variables['System.PullRequest.SourceBranch'], variables['Build.SourceBranch']), 'refs/heads/', '') ]"
	sourcesDirRoot = "$(Build.SourcesDirectory)"
)

// Provider renders azure-pipelines.yml.
type Provider struct{}

// New returns the Azure DevOps provider.
func New() *Provider { return &Provider{} }

// Name implements providers.Provider.
func (*Provider) Name() string { return Name }

// DisplayName implements providers.Provider.
func (*Provider) DisplayName() string { return "Azure DevOps Pipelines" }

// Description implements providers.Provider.
func (*Provider) Description() string { return "Azure DevOps YAML pipeline (azure-pipelines.yml)" }

// Capabilities implements providers.Provider.
func (*Provider) Capabilities() providers.CapabilitySet {
	return providers.NewCapabilitySet(
		providers.CapabilityInfo{ID: providers.CapPullRequestTrigger, Support: providers.Native,
			Note: "pr triggers apply to GitHub and Bitbucket repositories; Azure Repos uses branch policies"},
		providers.CapabilityInfo{ID: providers.CapRetries, Support: providers.Unsupported,
			Note: "Azure Pipelines cannot retry jobs; retry inside the step script or remove retries"},
		providers.CapabilityInfo{ID: providers.CapCache, Support: providers.Native, Note: "Cache@2 task; one path per cache step"},
		providers.CapabilityInfo{ID: providers.CapArtifactUpload, Support: providers.Native, Note: "publish step; one path per artifact"},
		providers.CapabilityInfo{ID: providers.CapArtifactDownload, Support: providers.Native, Note: "DownloadPipelineArtifact@2 into the sources directory"},
		providers.CapabilityInfo{ID: providers.CapTestResults, Support: providers.Native, Note: "PublishTestResults@2, run even when earlier steps fail"},
		providers.CapabilityInfo{ID: providers.CapImageBuild, Support: providers.Emulated, Note: "runs the docker CLI"},
		providers.CapabilityInfo{ID: providers.CapImagePush, Support: providers.Emulated, Note: "runs the docker CLI"},
		providers.CapabilityInfo{ID: providers.CapKubernetesDeploy, Support: providers.Emulated, Note: "runs kubectl apply"},
		providers.CapabilityInfo{ID: providers.CapReusableSteps, Support: providers.Native, Note: "references must be Azure tasks: Name@MajorVersion"},
		providers.CapabilityInfo{ID: providers.CapEnvironments, Support: providers.Native, Note: "jobs with an environment become deployment jobs"},
		providers.CapabilityInfo{ID: providers.CapApprovals, Support: providers.ProviderManaged,
			Note: "add an Approvals check to the environment under Pipelines > Environments"},
	)
}

type pool struct {
	Name    string   `yaml:"name"`
	VMImage string   `yaml:"vmImage"`
	Demands []string `yaml:"demands"`
}

type extension struct {
	Pools map[string]pool `yaml:"pools"`
}

type jobExtension struct {
	Pool *pool `yaml:"pool"`
}

type config struct {
	ext  extension
	jobs map[*pipeline.Job]jobExtension
}

var (
	taskRe     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]*@[0-9]+$`)
	poolNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]*$`)
	demandRe   = regexp.MustCompile(`^[A-Za-z0-9._ -]+(?: -equals [A-Za-z0-9._ -]+)?$`)
	upperRe    = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
)

func decode(p *pipeline.Pipeline) (config, diagnostics.List) {
	var diags diagnostics.List
	cfg := config{jobs: map[*pipeline.Job]jobExtension{}}
	if n := p.Extensions.Get(Name); n != nil {
		path := pipeline.JoinKey("extensions", Name)
		diags = append(diags, pipeline.DecodeStrict(n, &cfg.ext, path, nil)...)
		for _, os := range sortedKeys(cfg.ext.Pools) {
			pp := pipeline.JoinKey(path+".pools", os)
			if os != pipeline.OSLinux && os != pipeline.OSWindows && os != pipeline.OSMacOS {
				diags = append(diags, diagnostics.Errorf(pp, "unknown runner %q", os).WithHint("use linux, windows, or macos"))
			}
			diags = append(diags, checkPool(pp, cfg.ext.Pools[os])...)
		}
	}
	for _, j := range p.AllJobs() {
		if n := j.Extensions.Get(Name); n != nil {
			path := pipeline.JoinKey(j.Path()+".extensions", Name)
			var je jobExtension
			diags = append(diags, pipeline.DecodeStrict(n, &je, path, nil)...)
			if je.Pool != nil {
				diags = append(diags, checkPool(path+".pool", *je.Pool)...)
			}
			cfg.jobs[j] = je
		}
	}
	for i := range diags {
		diags[i].Provider = Name
	}
	return cfg, diags
}

func checkPool(path string, p pool) diagnostics.List {
	var diags diagnostics.List
	if (p.Name == "") == (p.VMImage == "") {
		diags = append(diags, diagnostics.Errorf(path, "a pool needs exactly one of name or vmImage"))
	}
	if p.Name != "" && !poolNameRe.MatchString(p.Name) {
		diags = append(diags, diagnostics.Errorf(path+".name", "invalid pool name %q", p.Name))
	}
	if p.VMImage != "" && !poolNameRe.MatchString(p.VMImage) {
		diags = append(diags, diagnostics.Errorf(path+".vmImage", "invalid vmImage %q", p.VMImage))
	}
	for i, d := range p.Demands {
		if !demandRe.MatchString(d) {
			diags = append(diags, diagnostics.Errorf(pipeline.JoinIndex(path+".demands", i), "invalid demand %q", d))
		}
	}
	return diags
}

// Validate implements providers.Provider.
func (pr *Provider) Validate(p *pipeline.Pipeline) diagnostics.List {
	diags := providers.CheckCapabilities(p, pr)
	_, extDiags := decode(p)
	diags = append(diags, extDiags...)
	add := func(d diagnostics.Diagnostic) { diags = append(diags, d.WithProvider(Name)) }

	for _, j := range p.AllJobs() {
		if j.TimeoutMinutes > 360 {
			add(diagnostics.Warningf(j.Path()+".timeoutMinutes", "Microsoft-hosted agents stop jobs after 360 minutes; %d applies only on self-hosted agents", j.TimeoutMinutes))
		}
	}
	// Azure does not pass variables with these prefixes to tasks and scripts.
	reserved := func(path, kind, name string) {
		for _, prefix := range []string{"ENDPOINT", "INPUT", "SECRET", "PATH", "SECUREFILE"} {
			if strings.HasPrefix(strings.ToUpper(name), prefix) {
				add(diagnostics.Errorf(path, "%s %q starts with %q, a prefix Azure Pipelines reserves, so scripts would not receive it", kind, name, strings.ToLower(prefix)).
					WithHint("rename it; Azure reserves names starting with endpoint, input, secret, path, and securefile in any case"))
				return
			}
		}
	}
	for _, v := range p.Variables {
		reserved(pipeline.JoinKey("variables", v.Name), "variable", v.Name)
	}
	for i, s := range p.Secrets {
		reserved(pipeline.JoinIndex("secrets", i), "secret", s)
	}
	for _, j := range p.AllJobs() {
		for _, e := range j.Env {
			reserved(pipeline.JoinKey(j.Path()+".env", e.Name), "env name", e.Name)
		}
		for _, axis := range j.Matrix {
			reserved(pipeline.JoinKey(j.Path()+".matrix", axis.Name), "matrix axis", axis.Name)
		}
	}
	for _, j := range p.AllJobs() {
		jp := j.Path()
		if j.Environment != nil {
			if len(j.Matrix) > 0 {
				add(diagnostics.Errorf(jp+".matrix", "Azure deployment jobs (jobs with an environment) cannot use a matrix"))
			}
			if j.Environment.URL != "" {
				add(diagnostics.Warningf(jp+".environment.url", "Azure Pipelines environments have no deployment URL; the url is dropped").
					WithHint("remove url or record it in the environment description"))
			}
		}
		for _, e := range j.Env {
			if !upperRe.MatchString(e.Name) {
				add(diagnostics.Warningf(pipeline.JoinKey(jp+".env", e.Name),
					"Azure exposes job variables as upper-case environment variables, so scripts see %s", strings.ToUpper(e.Name)).
					WithHint("use an upper-case name so every provider exposes the same variable"))
			}
		}
		for _, s := range j.Steps {
			sp := s.Path()
			switch s.Kind() {
			case pipeline.StepUses:
				if !taskRe.MatchString(s.Uses) {
					add(diagnostics.Errorf(sp+".uses", "%q is not an Azure task reference", s.Uses).
						WithHint("use Name@MajorVersion, e.g. NodeTool@0"))
				}
			case pipeline.StepCache:
				if len(s.Cache.Paths) > 1 {
					add(diagnostics.Errorf(sp+".cache.paths", "the Azure Cache@2 task caches a single path").
						WithHint("split the paths into separate cache steps with distinct keys"))
				}
				if len(s.Cache.Files) == 0 {
					add(diagnostics.Warningf(sp+".cache", "Azure caches are immutable, so a cache key without files is saved once and never refreshed").
						WithHint("list the lock files that should invalidate the cache under cache.files"))
				}
			case pipeline.StepUploadArtifact:
				if len(s.UploadArtifact.Paths) > 1 {
					add(diagnostics.Errorf(sp+".uploadArtifact.paths", "Azure pipeline artifacts publish a single path").
						WithHint("upload a common parent directory or split into separate artifacts"))
				}
			}
		}
	}
	return diags
}

// Render implements providers.Provider.
func (pr *Provider) Render(_ context.Context, p *pipeline.Pipeline, _ providers.RenderOptions) ([]providers.GeneratedFile, error) {
	cfg, diags := decode(p)
	if diags.HasErrors() {
		return nil, fmt.Errorf("azure-devops: invalid extensions")
	}
	r := &renderer{p: p, cfg: cfg, artifacts: compiler.Artifacts(p)}
	root := shared.NewMap()
	r.triggers(root)
	if len(p.Variables) > 0 {
		vars := shared.NewMap()
		for _, v := range p.Variables {
			vars.Set(v.Name, v.Value)
		}
		root.Set("variables", vars)
	}
	var stages []*shared.Map
	for _, s := range p.Order {
		stages = append(stages, r.stage(s))
	}
	root.Set("stages", stages)
	body, err := shared.Encode(root)
	if err != nil {
		return nil, err
	}
	body = append(body, '\n')
	body = append(body, render.CustomBlock(providers.CommentHash, "")...)
	return []providers.GeneratedFile{{Path: "azure-pipelines.yml", Content: body, Comment: providers.CommentHash, YAML: true,
		CustomKeys: providers.CustomKeyPolicy{Allowed: []string{"name", "lockBehavior", "appendCommitMessageToRunName"}}}}, nil
}

type renderer struct {
	p         *pipeline.Pipeline
	cfg       config
	artifacts map[string]compiler.Artifact
}

func ident(name string) string { return strings.ReplaceAll(name, "-", "_") }

func include(pats []string) *shared.Map {
	return shared.NewMap().Set("include", shared.FlowSeq(pats))
}

func (r *renderer) triggers(root *shared.Map) {
	t := r.p.Triggers
	switch {
	case t.Push == nil:
		root.Set("trigger", "none")
	case len(t.Push.Branches) == 0 && len(t.Push.Tags) == 0:
		root.Set("trigger", shared.NewMap().Set("branches", include([]string{"*"})).Set("tags", include([]string{"*"})))
	default:
		trig := shared.NewMap()
		if len(t.Push.Branches) > 0 {
			trig.Set("branches", include(t.Push.Branches))
		} else {
			trig.Set("branches", shared.NewMap().Set("exclude", shared.FlowSeq([]string{"*"})))
		}
		if len(t.Push.Tags) > 0 {
			trig.Set("tags", include(t.Push.Tags))
		}
		root.Set("trigger", trig)
	}
	switch {
	case t.PullRequest == nil:
		root.Set("pr", "none")
	case len(t.PullRequest.Branches) == 0:
		root.Set("pr", shared.NewMap().Set("branches", include([]string{"*"})))
	default:
		root.Set("pr", shared.NewMap().Set("branches", include(t.PullRequest.Branches)))
	}
	if len(t.Schedules) > 0 {
		var scheds []*shared.Map
		for _, s := range t.Schedules {
			scheds = append(scheds, shared.NewMap().
				Set("cron", shared.Quoted(s.Cron)).
				Set("displayName", "Scheduled "+s.Cron).
				Set("branches", include(s.Branches)).
				Set("always", true))
		}
		root.Set("schedules", scheds)
	}
}

var reasons = map[string][]string{
	pipeline.EventPush:        {"IndividualCI", "BatchedCI"},
	pipeline.EventPullRequest: {"PullRequest"},
	pipeline.EventManual:      {"Manual"},
	pipeline.EventSchedule:    {"Schedule"},
}

// condition renders an Azure condition; status is succeeded() or succeededOrFailed().
func condition(status string, conds ...*pipeline.Condition) string {
	parts := []string{status}
	for _, c := range conds {
		if c.IsEmpty() {
			continue
		}
		if len(c.Branches) > 0 {
			var alts []string
			for _, b := range c.Branches {
				if lit, prefix := pipeline.PatternPrefix(b); prefix {
					alts = append(alts, fmt.Sprintf("startsWith(variables['Build.SourceBranch'], 'refs/heads/%s')", lit))
				} else {
					alts = append(alts, fmt.Sprintf("eq(variables['Build.SourceBranch'], 'refs/heads/%s')", b))
				}
			}
			if len(alts) == 1 {
				parts = append(parts, alts[0])
			} else {
				parts = append(parts, "or("+strings.Join(alts, ", ")+")")
			}
		}
		if len(c.Events) > 0 {
			var quoted []string
			for _, e := range c.Events {
				for _, reason := range reasons[e] {
					quoted = append(quoted, "'"+reason+"'")
				}
			}
			parts = append(parts, "in(variables['Build.Reason'], "+strings.Join(quoted, ", ")+")")
		}
	}
	if len(parts) == 1 {
		return status
	}
	return "and(" + strings.Join(parts, ", ") + ")"
}

func (r *renderer) stage(s *pipeline.Stage) *shared.Map {
	m := shared.NewMap()
	m.Set("stage", ident(s.Name))
	m.Set("displayName", s.Name)
	var deps []string
	for _, d := range s.Needs {
		deps = append(deps, ident(d.Name))
	}
	m.Set("dependsOn", shared.FlowSeq(deps))
	if !s.When.IsEmpty() {
		m.Set("condition", condition("succeeded()", s.When))
	}
	var jobs []*shared.Map
	for _, j := range s.JobOrder {
		jobs = append(jobs, r.job(j))
	}
	m.Set("jobs", jobs)
	return m
}

func (r *renderer) pool(j *pipeline.Job) *shared.Map {
	var p pool
	if je, ok := r.cfg.jobs[j]; ok && je.Pool != nil {
		p = *je.Pool
	} else if cp, ok := r.cfg.ext.Pools[j.OS()]; ok {
		p = cp
	} else {
		p.VMImage = map[string]string{pipeline.OSLinux: "ubuntu-latest", pipeline.OSWindows: "windows-latest",
			pipeline.OSMacOS: "macOS-latest"}[j.OS()]
	}
	m := shared.NewMap()
	if p.Name != "" {
		m.Set("name", p.Name)
	} else {
		m.Set("vmImage", p.VMImage)
	}
	if len(p.Demands) > 0 {
		m.Set("demands", p.Demands)
	}
	return m
}

// usesBranch reports whether any step of j renders ${git.branch}.
func usesBranch(j *pipeline.Job) bool {
	for _, s := range j.Steps {
		for _, ref := range stepRefs(s) {
			if ref.Namespace == pipeline.NSGit && ref.Name == pipeline.GitBranch {
				return true
			}
		}
	}
	for _, ref := range shared.TextRefs(envValues(j.Env)...) {
		if ref.Namespace == pipeline.NSGit && ref.Name == pipeline.GitBranch {
			return true
		}
	}
	return false
}

func envValues(v pipeline.Vars) []string {
	out := make([]string, len(v))
	for i, e := range v {
		out[i] = e.Value
	}
	return out
}

func stepRefs(s *pipeline.Step) []pipeline.Ref {
	texts := envValues(s.Env)
	texts = append(texts, envValues(s.With)...)
	return append(shared.CommandRefs(s), shared.TextRefs(texts...)...)
}

// matrixNames returns unique Azure identifiers for each matrix combination,
// e.g. python_3_11.
func matrixNames(axes pipeline.Matrix, combos [][]string) []string {
	names := make([]string, len(combos))
	used := map[string]bool{}
	for i, c := range combos {
		var parts []string
		for k, v := range c {
			parts = append(parts, axes[k].Name, v)
		}
		name := strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
				return r
			}
			return '_'
		}, strings.Join(parts, "_"))
		// Azure compares leg names ignoring case.
		base := name
		for n := 2; used[strings.ToLower(name)]; n++ {
			name = fmt.Sprintf("%s_%d", base, n)
		}
		used[strings.ToLower(name)] = true
		names[i] = name
	}
	return names
}

// imageFor resolves a container image template for one matrix combination.
func (r *renderer) imageFor(image string, axes pipeline.Matrix, combo []string) string {
	var b strings.Builder
	for _, seg := range pipeline.MustTemplate(image) {
		switch {
		case seg.Ref == nil:
			b.WriteString(seg.Literal)
		case seg.Ref.Namespace == pipeline.NSVariables:
			v, _ := r.p.Variables.Get(seg.Ref.Name)
			b.WriteString(v)
		case seg.Ref.Namespace == pipeline.NSMatrix:
			for i, a := range axes {
				if a.Name == seg.Ref.Name {
					b.WriteString(combo[i])
				}
			}
		}
	}
	return b.String()
}

func (r *renderer) job(j *pipeline.Job) *shared.Map {
	m := shared.NewMap()
	deployment := j.Environment != nil
	if deployment {
		m.Set("deployment", ident(j.Name))
	} else {
		m.Set("job", ident(j.Name))
	}
	m.Set("displayName", j.Name)
	var deps []string
	for _, d := range j.SameStageNeeds {
		deps = append(deps, ident(d.Name))
	}
	m.Set("dependsOn", shared.FlowSeq(deps))
	// A stage succeeds even when a job in it was skipped, so jobs carry the
	// conditions of upstream jobs to skip like their dependencies did.
	var conds []*pipeline.Condition
	for _, c := range shared.InheritedConditions(r.p, j) {
		if c != j.Stage.When {
			conds = append(conds, c)
		}
	}
	if len(conds) > 0 {
		m.Set("condition", condition("succeeded()", conds...))
	}
	if deployment {
		m.Set("environment", j.Environment.Name)
	}
	m.Set("pool", r.pool(j))

	matrixImage := j.Container != nil && len(j.Matrix) > 0 && !pipeline.MustTemplate(j.Container.Image).IsLiteral() &&
		hasMatrixRef(j.Container.Image)
	if c := j.Container; c != nil {
		image := r.imageFor(c.Image, nil, nil)
		if matrixImage {
			image = "$[ variables['" + containerVar + "'] ]"
		}
		if c.Options != "" {
			m.Set("container", shared.NewMap().Set("image", image).Set("options", c.Options))
		} else {
			m.Set("container", image)
		}
	}
	if j.TimeoutMinutes > 0 {
		m.Set("timeoutInMinutes", j.TimeoutMinutes)
	}
	if len(j.Matrix) > 0 {
		combos := j.Matrix.Combinations()
		names := matrixNames(j.Matrix, combos)
		mx := shared.NewMap()
		for i, combo := range combos {
			entry := shared.NewMap()
			for k, axis := range j.Matrix {
				entry.Set(axis.Name, combo[k])
			}
			if matrixImage {
				entry.Set(containerVar, r.imageFor(j.Container.Image, j.Matrix, combo))
			}
			mx.Set(names[i], entry)
		}
		m.Set("strategy", shared.NewMap().Set("matrix", mx))
	}
	vars := shared.NewMap()
	for _, e := range j.Env {
		vars.Set(e.Name, macro(e.Value))
	}
	if usesBranch(j) {
		vars.Set(branchVar, branchRuntime)
	}
	if vars.Len() > 0 {
		m.Set("variables", vars)
	}

	var steps []*shared.Map
	if deployment {
		steps = append(steps, shared.NewMap().Set("download", "none"))
	} else if !hasCheckout(j) {
		steps = append(steps, shared.NewMap().Set("checkout", "none"))
	}
	for _, s := range j.Steps {
		steps = append(steps, r.step(j, s))
	}
	if deployment {
		m.Set("strategy", shared.NewMap().Set("runOnce", shared.NewMap().Set("deploy", shared.NewMap().Set("steps", steps))))
	} else {
		m.Set("steps", steps)
	}
	return m
}

func hasMatrixRef(s string) bool {
	for _, ref := range pipeline.MustTemplate(s).Refs() {
		if ref.Namespace == pipeline.NSMatrix {
			return true
		}
	}
	return false
}

func hasCheckout(j *pipeline.Job) bool {
	for _, s := range j.Steps {
		if s.Kind() == pipeline.StepCheckout {
			return true
		}
	}
	return false
}

// macro renders a template with $(name) macros, for task inputs and env values.
func macro(value string) string {
	var b strings.Builder
	for _, seg := range pipeline.MustTemplate(value) {
		if seg.Ref == nil {
			b.WriteString(seg.Literal)
			continue
		}
		switch {
		case seg.Ref.Namespace == pipeline.NSGit && seg.Ref.Name == pipeline.GitSHA:
			b.WriteString("$(Build.SourceVersion)")
		case seg.Ref.Namespace == pipeline.NSGit:
			b.WriteString("$(" + branchVar + ")")
		default:
			b.WriteString("$(" + seg.Ref.Name + ")")
		}
	}
	return b.String()
}

// envName maps references to the environment variables scripts read. Azure
// exposes variables upper-cased; secrets are bound explicitly under their own name.
func envName(ref pipeline.Ref) string {
	switch ref.Namespace {
	case pipeline.NSGit:
		if ref.Name == pipeline.GitBranch {
			return branchVar
		}
		return "BUILD_SOURCEVERSION"
	case pipeline.NSSecrets:
		return ref.Name
	default:
		return strings.ToUpper(ref.Name)
	}
}

func (r *renderer) step(j *pipeline.Job, s *pipeline.Step) *shared.Map {
	m := shared.NewMap()
	kind := s.Kind()
	status := "succeeded()"
	if kind == pipeline.StepPublishTestResults {
		status = "succeededOrFailed()"
	}
	switch kind {
	case pipeline.StepCheckout:
		m.Set("checkout", "self")
	case pipeline.StepUses:
		m.Set("task", s.Uses)
		if len(s.With) > 0 {
			inputs := shared.NewMap()
			for _, w := range s.With {
				inputs.Set(w.Name, macro(w.Value))
			}
			m.Set("inputs", inputs)
		}
	case pipeline.StepCache:
		key := []string{`"` + s.Cache.Key + `"`, `"$(Agent.OS)"`}
		key = append(key, s.Cache.Files...)
		m.Set("task", "Cache@2")
		m.Set("inputs", shared.NewMap().Set("key", strings.Join(key, " | ")).Set("path", s.Cache.Paths[0]))
	case pipeline.StepUploadArtifact:
		m.Set("publish", s.UploadArtifact.Paths[0])
		m.Set("artifact", s.UploadArtifact.Name)
	case pipeline.StepDownloadArtifact:
		a := r.artifacts[s.DownloadArtifact.Name]
		target := sourcesDirRoot
		if root := shared.UploadRoot(a.Step.UploadArtifact.Paths); root != "." {
			target += "/" + root
		}
		m.Set("task", "DownloadPipelineArtifact@2")
		m.Set("inputs", shared.NewMap().
			Set("buildType", "current").
			Set("artifactName", s.DownloadArtifact.Name).
			Set("targetPath", target))
	case pipeline.StepPublishTestResults:
		m.Set("task", "PublishTestResults@2")
		m.Set("inputs", shared.NewMap().
			Set("testResultsFormat", "JUnit").
			Set("testResultsFiles", s.PublishTestResults.Path))
	default:
		shell := s.ShellFor(j.OS())
		script := shared.Command(s, shell, envName)
		switch shell {
		case pipeline.ShellPwsh:
			m.Set("pwsh", shared.Block(script))
		case pipeline.ShellSh:
			m.Set("bash", shared.Block("set -e\n"+script))
		default:
			m.Set("bash", shared.Block("set -eo pipefail\n"+script))
		}
	}
	if name := shared.DisplayName(s); name != "" && kind != pipeline.StepCheckout {
		m.Set("displayName", name)
	}
	if !s.When.IsEmpty() || status != "succeeded()" {
		m.Set("condition", condition(status, s.When))
	}
	if s.WorkingDirectory != "" {
		m.Set("workingDirectory", s.WorkingDirectory)
	}
	env := shared.NewMap()
	for _, e := range s.Env {
		env.Set(e.Name, macro(e.Value))
	}
	if shared.IsCommand(s) {
		for _, ref := range shared.CommandRefs(s) {
			if ref.Namespace == pipeline.NSSecrets {
				env.Set(ref.Name, "$("+ref.Name+")")
			}
		}
	}
	if env.Len() > 0 {
		m.Set("env", env)
	}
	return m
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
