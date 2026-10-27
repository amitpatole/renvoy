// Package githubactions renders pipelines as GitHub Actions workflows.
package githubactions

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"slices"
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
const Name = "github-actions"

// Pinned first-party actions.
const (
	actionCheckout = "actions/checkout@v7"
	actionUpload   = "actions/upload-artifact@v7"
	actionDownload = "actions/download-artifact@v8"
	actionCache    = "actions/cache@v6"
)

// Provider renders GitHub Actions workflows.
type Provider struct{}

// New returns the GitHub Actions provider.
func New() *Provider { return &Provider{} }

// Name implements providers.Provider.
func (*Provider) Name() string { return Name }

// DisplayName implements providers.Provider.
func (*Provider) DisplayName() string { return "GitHub Actions" }

// Description implements providers.Provider.
func (*Provider) Description() string {
	return "GitHub Actions workflow (.github/workflows/<name>.yml)"
}

// Capabilities implements providers.Provider.
func (*Provider) Capabilities() providers.CapabilitySet {
	return providers.NewCapabilitySet(
		providers.CapabilityInfo{ID: providers.CapScheduleTrigger, Support: providers.Native,
			Note: "scheduled workflows run on the repository's default branch only"},
		providers.CapabilityInfo{ID: providers.CapRetries, Support: providers.Unsupported,
			Note: "GitHub Actions cannot retry jobs; retry inside the step script or remove retries"},
		providers.CapabilityInfo{ID: providers.CapTestResults, Support: providers.Emulated,
			Note: "uploaded as an artifact named test-results-<job>; GitHub has no built-in test report view"},
		providers.CapabilityInfo{ID: providers.CapImageBuild, Support: providers.Emulated, Note: "runs the docker CLI"},
		providers.CapabilityInfo{ID: providers.CapImagePush, Support: providers.Emulated, Note: "runs the docker CLI"},
		providers.CapabilityInfo{ID: providers.CapKubernetesDeploy, Support: providers.Emulated, Note: "runs kubectl apply"},
		providers.CapabilityInfo{ID: providers.CapReusableSteps, Support: providers.Native,
			Note: "references must be GitHub actions: owner/repo@ref, owner/repo/path@ref, ./path, or docker://image"},
		providers.CapabilityInfo{ID: providers.CapApprovals, Support: providers.ProviderManaged,
			Note: "add required reviewers to the environment in the repository settings"},
	)
}

type extension struct {
	Permissions map[string]string   `yaml:"permissions"`
	Runners     map[string][]string `yaml:"runners"`
}

type jobExtension struct {
	Permissions map[string]string `yaml:"permissions"`
	RunsOn      []string          `yaml:"runsOn"`
}

type config struct {
	ext  extension
	jobs map[*pipeline.Job]jobExtension
}

var (
	permissionScopes = []string{"actions", "attestations", "checks", "contents", "deployments", "discussions",
		"id-token", "issues", "models", "packages", "pages", "pull-requests", "repository-projects",
		"security-events", "statuses"}
	runnerLabelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	actionRefRe   = regexp.MustCompile(`^(?:[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_./-]+)?@[A-Za-z0-9_./-]+|\./[A-Za-z0-9_./-]+|docker://[A-Za-z0-9_./:@-]+)$`)
)

func decode(p *pipeline.Pipeline) (config, diagnostics.List) {
	var diags diagnostics.List
	cfg := config{jobs: map[*pipeline.Job]jobExtension{}}
	if n := p.Extensions.Get(Name); n != nil {
		path := pipeline.JoinKey("extensions", Name)
		diags = append(diags, pipeline.DecodeStrict(n, &cfg.ext, path, nil)...)
		diags = append(diags, checkPermissions(path+".permissions", cfg.ext.Permissions)...)
		for _, os := range sortedKeys(cfg.ext.Runners) {
			rp := pipeline.JoinKey(path+".runners", os)
			if os != pipeline.OSLinux && os != pipeline.OSWindows && os != pipeline.OSMacOS {
				diags = append(diags, diagnostics.Errorf(rp, "unknown runner %q", os).WithHint("use linux, windows, or macos"))
			}
			diags = append(diags, checkLabels(rp, cfg.ext.Runners[os])...)
		}
	}
	for _, j := range p.AllJobs() {
		n := j.Extensions.Get(Name)
		if n == nil {
			continue
		}
		path := pipeline.JoinKey(j.Path()+".extensions", Name)
		var je jobExtension
		diags = append(diags, pipeline.DecodeStrict(n, &je, path, nil)...)
		diags = append(diags, checkPermissions(path+".permissions", je.Permissions)...)
		if je.RunsOn != nil {
			diags = append(diags, checkLabels(path+".runsOn", je.RunsOn)...)
		}
		cfg.jobs[j] = je
	}
	for i := range diags {
		diags[i].Provider = Name
	}
	return cfg, diags
}

func checkPermissions(path string, perms map[string]string) diagnostics.List {
	var diags diagnostics.List
	for _, k := range sortedKeys(perms) {
		kp := pipeline.JoinKey(path, k)
		if !slices.Contains(permissionScopes, k) {
			diags = append(diags, diagnostics.Errorf(kp, "unknown GitHub token permission %q", k))
		}
		if v := perms[k]; v != "read" && v != "write" && v != "none" {
			diags = append(diags, diagnostics.Errorf(kp, "permission %q must be read, write, or none", k))
		}
	}
	return diags
}

func checkLabels(path string, labels []string) diagnostics.List {
	if len(labels) == 0 {
		return diagnostics.List{diagnostics.Errorf(path, "at least one runner label is required")}
	}
	var diags diagnostics.List
	for i, l := range labels {
		if !runnerLabelRe.MatchString(l) {
			diags = append(diags, diagnostics.Errorf(pipeline.JoinIndex(path, i), "invalid runner label %q", l))
		}
	}
	return diags
}

// Validate implements providers.Provider.
func (pr *Provider) Validate(p *pipeline.Pipeline) diagnostics.List {
	diags := providers.CheckCapabilities(p, pr)
	cfg, extDiags := decode(p)
	diags = append(diags, extDiags...)
	add := func(d diagnostics.Diagnostic) { diags = append(diags, d.WithProvider(Name)) }

	for i, s := range p.Secrets {
		if strings.HasPrefix(strings.ToUpper(s), "GITHUB_") {
			add(diagnostics.Errorf(pipeline.JoinIndex("secrets", i), "GitHub does not allow secret names starting with GITHUB_"))
		}
	}
	defaultVar := func(path, name string) {
		if up := strings.ToUpper(name); strings.HasPrefix(up, "GITHUB_") || strings.HasPrefix(up, "RUNNER_") {
			add(diagnostics.Warningf(path, "GitHub does not let workflows override default variables named GITHUB_* or RUNNER_*, so %q may be ignored", name).
				WithHint("rename it"))
		}
	}
	for _, v := range p.Variables {
		defaultVar(pipeline.JoinKey("variables", v.Name), v.Name)
	}
	for _, j := range p.AllJobs() {
		for _, e := range j.Env {
			defaultVar(pipeline.JoinKey(j.Path()+".env", e.Name), e.Name)
		}
		for _, s := range j.Steps {
			for _, e := range s.Env {
				defaultVar(pipeline.JoinKey(s.Path()+".env", e.Name), e.Name)
			}
		}
		for _, axis := range j.Matrix {
			if strings.EqualFold(axis.Name, "include") || strings.EqualFold(axis.Name, "exclude") {
				add(diagnostics.Errorf(pipeline.JoinKey(j.Path()+".matrix", axis.Name),
					"GitHub Actions reserves the matrix key %q for lists of combinations", strings.ToLower(axis.Name)).
					WithHint("rename the axis"))
			}
		}
	}
	for _, j := range p.AllJobs() {
		if j.Container != nil {
			for _, opt := range strings.Fields(j.Container.Options) {
				name, _, _ := strings.Cut(opt, "=")
				if name == "--network" || name == "--net" || name == "--entrypoint" {
					add(diagnostics.Errorf(j.Path()+".container.options", "GitHub Actions does not support the %s container option", name).
						WithHint("remove it; GitHub sets the container network and entrypoint itself"))
				}
			}
		}
		if j.TimeoutMinutes > maxHostedMinutes {
			add(diagnostics.Warningf(j.Path()+".timeoutMinutes", "GitHub-hosted runners stop jobs after %d minutes; %d applies only on self-hosted runners", maxHostedMinutes, j.TimeoutMinutes))
		}
	}
	for i, s := range p.Triggers.Schedules {
		if minGap(pipeline.CronMinutes(s.Cron)) < 5 {
			add(diagnostics.Warningf(pipeline.JoinIndex("triggers.schedules", i)+".cron",
				"GitHub runs scheduled workflows at most once every 5 minutes, so %q fires less often than written", s.Cron))
		}
	}
	for _, j := range p.AllJobs() {
		uploads := 0
		for _, s := range j.Steps {
			if s.UploadArtifact != nil {
				uploads++
			}
		}
		if uploads > maxJobArtifacts {
			add(diagnostics.Errorf(j.Path()+".steps", "the job uploads %d artifacts; actions/upload-artifact allows at most %d per job", uploads, maxJobArtifacts))
		}
	}
	for i := range p.Triggers.Schedules {
		add(diagnostics.Warningf(pipeline.JoinIndex("triggers.schedules", i),
			"GitHub runs scheduled workflows only on the default branch; the branches list is not enforced").
			WithHint("make sure the listed branch is the repository's default branch"))
	}
	if cfg.ext.Permissions == nil {
		add(diagnostics.Infof("", "the workflow token is limited to contents: read").
			WithHint("grant more with extensions.github-actions.permissions, e.g. packages: write to push images"))
	}
	for _, j := range p.AllJobs() {
		for _, s := range j.Steps {
			switch s.Kind() {
			case pipeline.StepUses:
				if !actionRefRe.MatchString(s.Uses) {
					add(diagnostics.Errorf(s.Path()+".uses", "%q is not a GitHub action reference", s.Uses).
						WithHint("use owner/repo@ref, owner/repo/path@ref, ./path, or docker://image"))
				}
			case pipeline.StepCache:
				if len(s.Cache.Files) == 0 {
					add(diagnostics.Warningf(s.Path()+".cache", "GitHub caches are immutable, so a cache key without files is saved once and never refreshed").
						WithHint("list the lock files that should invalidate the cache under cache.files"))
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
		return nil, fmt.Errorf("github-actions: invalid extensions")
	}
	r := &renderer{p: p, cfg: cfg, artifacts: compiler.Artifacts(p)}
	root := shared.NewMap()
	root.Set("name", p.Metadata.Name)
	root.SetPlainKey("on", r.triggers())

	perms := shared.NewMap()
	if cfg.ext.Permissions != nil {
		for _, k := range sortedKeys(cfg.ext.Permissions) {
			perms.Set(k, cfg.ext.Permissions[k])
		}
	} else {
		perms.Set("contents", "read")
	}
	root.Set("permissions", perms)

	if len(p.Variables) > 0 {
		env := shared.NewMap()
		for _, v := range p.Variables {
			env.Set(v.Name, v.Value)
		}
		root.Set("env", env)
	}
	if r.usesShell(nil) {
		root.Set("defaults", shared.NewMap().Set("run", shared.NewMap().Set("shell", pipeline.ShellBash)))
	}
	jobs := shared.NewMap()
	for _, j := range p.OrderedJobs() {
		jobs.Set(j.Name, r.job(j))
	}
	root.Set("jobs", jobs)

	body, err := shared.Encode(root, "jobs")
	if err != nil {
		return nil, err
	}
	body = bytes.Replace(body, []byte("\n\"on\":"), []byte("\non:"), 1)
	body = append(body, '\n')
	body = append(body, render.CustomBlock(providers.CommentHash, "")...)
	return []providers.GeneratedFile{{
		Path:    ".github/workflows/" + p.Metadata.Name + ".yml",
		Content: body,
		Comment: providers.CommentHash,
		YAML:    true,
		// GitHub does not start runs from workflow files over 500 KB.
		MaxBytes: 500 * 1024,
		// Every other top-level key (env, defaults, permissions, on) applies to the
		// generated jobs; jobs cannot be added because "jobs" is generated.
		CustomKeys: providers.CustomKeyPolicy{Allowed: []string{"concurrency", "run-name"}},
	}}, nil
}

type renderer struct {
	p         *pipeline.Pipeline
	cfg       config
	artifacts map[string]compiler.Artifact
}

func pattern(pat string) string {
	if lit, prefix := pipeline.PatternPrefix(pat); prefix {
		return lit + "**"
	}
	return pat
}

func patterns(pats []string) []string {
	out := make([]string, len(pats))
	for i, p := range pats {
		out[i] = pattern(p)
	}
	return out
}

func (r *renderer) triggers() *shared.Map {
	t := r.p.Triggers
	on := shared.NewMap()
	if t.Push != nil {
		if len(t.Push.Branches) == 0 && len(t.Push.Tags) == 0 {
			on.Set("push", shared.Null())
		} else {
			push := shared.NewMap()
			if len(t.Push.Branches) > 0 {
				push.Set("branches", shared.FlowSeq(patterns(t.Push.Branches)))
			}
			if len(t.Push.Tags) > 0 {
				push.Set("tags", shared.FlowSeq(patterns(t.Push.Tags)))
			}
			on.Set("push", push)
		}
	}
	if t.PullRequest != nil {
		if len(t.PullRequest.Branches) == 0 {
			on.Set("pull_request", shared.Null())
		} else {
			on.Set("pull_request", shared.NewMap().Set("branches", shared.FlowSeq(patterns(t.PullRequest.Branches))))
		}
	}
	if t.Manual != nil {
		on.Set("workflow_dispatch", shared.Null())
	}
	if len(t.Schedules) > 0 {
		var crons []*shared.Map
		for _, s := range t.Schedules {
			crons = append(crons, shared.NewMap().Set("cron", shared.Quoted(s.Cron)))
		}
		on.Set("schedule", crons)
	}
	return on
}

var eventNames = map[string]string{
	pipeline.EventPush:        "push",
	pipeline.EventPullRequest: "pull_request",
	pipeline.EventManual:      "workflow_dispatch",
	pipeline.EventSchedule:    "schedule",
}

// condition renders conditions as a GitHub expression body (without ${{ }}).
func condition(conds ...*pipeline.Condition) string {
	var parts []string
	for _, c := range conds {
		if c.IsEmpty() {
			continue
		}
		if len(c.Branches) > 0 {
			var alts []string
			for _, b := range c.Branches {
				if lit, prefix := pipeline.PatternPrefix(b); prefix {
					alts = append(alts, fmt.Sprintf("startsWith(github.ref, 'refs/heads/%s')", lit))
				} else {
					alts = append(alts, fmt.Sprintf("github.ref == 'refs/heads/%s'", b))
				}
			}
			parts = append(parts, group(alts))
		}
		if len(c.Events) > 0 {
			var alts []string
			for _, e := range c.Events {
				alts = append(alts, fmt.Sprintf("github.event_name == '%s'", eventNames[e]))
			}
			parts = append(parts, group(alts))
		}
	}
	if len(parts) == 1 && strings.HasPrefix(parts[0], "(") {
		return parts[0][1 : len(parts[0])-1]
	}
	return strings.Join(parts, " && ")
}

func group(alts []string) string {
	if len(alts) == 1 {
		return alts[0]
	}
	return "(" + strings.Join(alts, " || ") + ")"
}

func (r *renderer) labels(j *pipeline.Job) []string {
	if je, ok := r.cfg.jobs[j]; ok && je.RunsOn != nil {
		return je.RunsOn
	}
	if l, ok := r.cfg.ext.Runners[j.OS()]; ok {
		return l
	}
	switch j.OS() {
	case pipeline.OSWindows:
		return []string{"windows-latest"}
	case pipeline.OSMacOS:
		return []string{"macos-latest"}
	default:
		return []string{"ubuntu-latest"}
	}
}

// usesShell reports whether j (or any job when j is nil) has shell command steps.
func (r *renderer) usesShell(j *pipeline.Job) bool {
	jobs := []*pipeline.Job{j}
	if j == nil {
		jobs = r.p.AllJobs()
	}
	for _, job := range jobs {
		for _, s := range job.Steps {
			if shared.IsCommand(s) {
				return true
			}
		}
	}
	return false
}

func (r *renderer) job(j *pipeline.Job) *shared.Map {
	m := shared.NewMap()
	if len(j.Needs) > 0 {
		var needs []string
		for _, n := range j.Needs {
			needs = append(needs, n.Name)
		}
		m.Set("needs", shared.FlowSeq(needs))
	}
	if cond := condition(shared.OwnConditions(j)...); cond != "" {
		m.Set("if", cond)
	}
	if labels := r.labels(j); len(labels) == 1 {
		m.Set("runs-on", labels[0])
	} else {
		m.Set("runs-on", shared.FlowSeq(labels))
	}
	if je, ok := r.cfg.jobs[j]; ok && je.Permissions != nil {
		perms := shared.NewMap()
		for _, k := range sortedKeys(je.Permissions) {
			perms.Set(k, je.Permissions[k])
		}
		m.Set("permissions", perms)
	}
	if e := j.Environment; e != nil {
		if e.URL != "" {
			m.Set("environment", shared.NewMap().Set("name", e.Name).Set("url", e.URL))
		} else {
			m.Set("environment", e.Name)
		}
	}
	if c := j.Container; c != nil {
		cm := shared.NewMap().Set("image", r.containerImage(c.Image))
		if c.Options != "" {
			cm.Set("options", c.Options)
		}
		m.Set("container", cm)
	}
	if j.TimeoutMinutes > 0 {
		m.Set("timeout-minutes", j.TimeoutMinutes)
	}
	if len(j.Matrix) > 0 {
		mx := shared.NewMap()
		for _, axis := range j.Matrix {
			mx.Set(axis.Name, shared.FlowSeq(axis.Values))
		}
		m.Set("strategy", shared.NewMap().Set("fail-fast", false).Set("matrix", mx))
	}
	if j.OS() == pipeline.OSWindows && r.usesShell(j) {
		m.Set("defaults", shared.NewMap().Set("run", shared.NewMap().Set("shell", pipeline.ShellPwsh)))
	}
	if len(j.Env) > 0 {
		env := shared.NewMap()
		for _, e := range j.Env {
			env.Set(e.Name, expr(e.Value))
		}
		m.Set("env", env)
	}
	var steps []*shared.Map
	for _, s := range j.Steps {
		steps = append(steps, r.step(j, s))
	}
	m.Set("steps", steps)
	return m
}

// containerImage inlines variables because the env context is unavailable in
// jobs.<id>.container.
func (r *renderer) containerImage(image string) string {
	var b strings.Builder
	for _, seg := range pipeline.MustTemplate(image) {
		switch {
		case seg.Ref == nil:
			b.WriteString(seg.Literal)
		case seg.Ref.Namespace == pipeline.NSVariables:
			v, _ := r.p.Variables.Get(seg.Ref.Name)
			b.WriteString(v)
		default:
			b.WriteString(refExpr(*seg.Ref))
		}
	}
	return b.String()
}

func refExpr(ref pipeline.Ref) string {
	switch ref.Namespace {
	case pipeline.NSVariables:
		return "${{ env." + ref.Name + " }}"
	case pipeline.NSSecrets:
		return "${{ secrets." + ref.Name + " }}"
	case pipeline.NSMatrix:
		return "${{ matrix." + ref.Name + " }}"
	default:
		if ref.Name == pipeline.GitBranch {
			return "${{ github.head_ref || github.ref_name }}"
		}
		return "${{ github.sha }}"
	}
}

// expr renders a template as a GitHub value with ${{ }} expressions. It is only
// used where values are not evaluated by a shell (env, with).
func expr(value string) string {
	var b strings.Builder
	for _, seg := range pipeline.MustTemplate(value) {
		if seg.Ref != nil {
			b.WriteString(refExpr(*seg.Ref))
		} else {
			b.WriteString(seg.Literal)
		}
	}
	return b.String()
}

// envName maps references to the environment variables that carry them in scripts.
func envName(ref pipeline.Ref) string {
	switch ref.Namespace {
	case pipeline.NSGit:
		if ref.Name == pipeline.GitBranch {
			return "RENVOY_GIT_BRANCH"
		}
		return "GITHUB_SHA"
	default:
		return ref.Name
	}
}

// bindings returns step env entries for references that are not already in the
// environment: secrets, matrix values, and the branch name. Binding through env
// keeps untrusted values such as branch names out of the script text.
func bindings(s *pipeline.Step) []pipeline.Var {
	var out []pipeline.Var
	for _, ref := range shared.CommandRefs(s) {
		if ref.Namespace == pipeline.NSVariables || (ref.Namespace == pipeline.NSGit && ref.Name == pipeline.GitSHA) {
			continue
		}
		out = append(out, pipeline.Var{Name: envName(ref), Value: refExpr(ref)})
	}
	return out
}

func (r *renderer) step(j *pipeline.Job, s *pipeline.Step) *shared.Map {
	m := shared.NewMap()
	kind := s.Kind()
	if s.Name != "" || (shared.IsCommand(s) && kind != pipeline.StepRun) || kind == pipeline.StepPublishTestResults {
		m.Set("name", shared.DisplayName(s))
	}
	cond := condition(s.When)
	if kind == pipeline.StepPublishTestResults {
		cond = strings.TrimSuffix("!cancelled() && "+cond, " && ")
	}
	if cond != "" {
		m.Set("if", cond)
	}
	switch kind {
	case pipeline.StepCheckout:
		m.Set("uses", actionCheckout)
	case pipeline.StepUses:
		m.Set("uses", s.Uses)
		if len(s.With) > 0 {
			with := shared.NewMap()
			for _, w := range s.With {
				with.Set(w.Name, expr(w.Value))
			}
			m.Set("with", with)
		}
	case pipeline.StepCache:
		key := s.Cache.Key + "-${{ runner.os }}"
		if len(s.Cache.Files) > 0 {
			quoted := make([]string, len(s.Cache.Files))
			for i, f := range s.Cache.Files {
				quoted[i] = "'" + f + "'"
			}
			key += "-${{ hashFiles(" + strings.Join(quoted, ", ") + ") }}"
		}
		m.Set("uses", actionCache)
		m.Set("with", shared.NewMap().Set("key", key).Set("path", pathList(s.Cache.Paths)))
	case pipeline.StepUploadArtifact:
		m.Set("uses", actionUpload)
		m.Set("with", shared.NewMap().
			Set("name", s.UploadArtifact.Name).
			Set("path", pathList(s.UploadArtifact.Paths)).
			Set("if-no-files-found", "error"))
	case pipeline.StepDownloadArtifact:
		a := r.artifacts[s.DownloadArtifact.Name]
		m.Set("uses", actionDownload)
		m.Set("with", shared.NewMap().
			Set("name", s.DownloadArtifact.Name).
			Set("path", shared.UploadRoot(a.Step.UploadArtifact.Paths)))
	case pipeline.StepPublishTestResults:
		name := "test-results-" + j.Name
		if len(j.Matrix) > 0 {
			name += "-${{ strategy.job-index }}"
		}
		m.Set("uses", actionUpload)
		m.Set("with", shared.NewMap().
			Set("name", name).
			Set("path", s.PublishTestResults.Path).
			Set("if-no-files-found", "warn"))
	default:
		shell := s.ShellFor(j.OS())
		m.Set("run", shared.Block(shared.Command(s, shell, envName)))
		jobShell := pipeline.ShellBash
		if j.OS() == pipeline.OSWindows {
			jobShell = pipeline.ShellPwsh
		}
		if shell != jobShell {
			m.Set("shell", shell)
		}
		if s.WorkingDirectory != "" {
			m.Set("working-directory", s.WorkingDirectory)
		}
	}
	if binds := bindings(s); len(s.Env)+len(binds) > 0 {
		em := shared.NewMap()
		for _, e := range s.Env {
			em.Set(e.Name, expr(e.Value))
		}
		for _, b := range binds {
			em.Set(b.Name, b.Value)
		}
		m.Set("env", em)
	}
	return m
}

func pathList(paths []string) string {
	if len(paths) == 1 {
		return paths[0]
	}
	return strings.Join(paths, "\n") + "\n"
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// maxHostedMinutes is the longest job GitHub-hosted runners run.
const maxHostedMinutes = 360

// maxJobArtifacts is actions/upload-artifact's limit on artifacts per job.
const maxJobArtifacts = 500

// minGap returns the shortest interval, in minutes, between firing minutes,
// counting the wrap into the next hour; 60 when it fires at most hourly.
func minGap(minutes [60]bool) int {
	gap, first, prev := 60, -1, -1
	for m, on := range minutes {
		if !on {
			continue
		}
		if prev >= 0 {
			gap = min(gap, m-prev)
		} else {
			first = m
		}
		prev = m
	}
	if first >= 0 && first != prev {
		gap = min(gap, first+60-prev)
	}
	return gap
}
