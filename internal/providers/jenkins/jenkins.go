// Package jenkins renders pipelines as Jenkins Declarative Pipelines.
package jenkins

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/amitpatole/renvoy/internal/diagnostics"
	"github.com/amitpatole/renvoy/internal/pipeline"
	"github.com/amitpatole/renvoy/internal/providers"
	"github.com/amitpatole/renvoy/internal/providers/shared"
	"github.com/amitpatole/renvoy/internal/render"
)

// Name is the provider identifier and extension namespace.
const Name = "jenkins"

const branchVar = "RENVOY_GIT_BRANCH"

// Provider renders a Jenkinsfile.
type Provider struct{}

// New returns the Jenkins provider.
func New() *Provider { return &Provider{} }

// Name implements providers.Provider.
func (*Provider) Name() string { return Name }

// DisplayName implements providers.Provider.
func (*Provider) DisplayName() string { return "Jenkins Declarative Pipeline" }

// Description implements providers.Provider.
func (*Provider) Description() string { return "Jenkins Declarative Pipeline (Jenkinsfile)" }

// Capabilities implements providers.Provider.
func (*Provider) Capabilities() providers.CapabilitySet {
	return providers.NewCapabilitySet(
		providers.CapabilityInfo{ID: providers.CapPushTrigger, Support: providers.ProviderManaged,
			Note: "multibranch pipelines build pushed branches and tags through SCM webhooks or indexing"},
		providers.CapabilityInfo{ID: providers.CapTagTrigger, Support: providers.ProviderManaged,
			Note: "enable tag discovery on the multibranch pipeline"},
		providers.CapabilityInfo{ID: providers.CapPullRequestTrigger, Support: providers.ProviderManaged,
			Note: "enable pull request discovery on the multibranch pipeline"},
		providers.CapabilityInfo{ID: providers.CapManualTrigger, Support: providers.Native, Note: "Build Now is always available"},
		providers.CapabilityInfo{ID: providers.CapBranchFilters, Support: providers.ProviderManaged,
			Note: "restrict branch discovery in the multibranch pipeline's branch sources"},
		providers.CapabilityInfo{ID: providers.CapScheduleTrigger, Support: providers.Native,
			Note: "triggers { cron } guarded by branch, in UTC"},
		providers.CapabilityInfo{ID: providers.CapStageGraph, Support: providers.Emulated,
			Note: "stages run one after another in dependency order"},
		providers.CapabilityInfo{ID: providers.CapJobGraph, Support: providers.Emulated,
			Note: "jobs with dependencies run one after another; independent jobs run in parallel"},
		providers.CapabilityInfo{ID: providers.CapStepConditions, Support: providers.Unsupported,
			Note: "Declarative Pipeline has no step-level conditions; move the step into a job with a when condition"},
		providers.CapabilityInfo{ID: providers.CapContainer, Support: providers.Native, Note: "docker agent (Docker Pipeline plugin)"},
		providers.CapabilityInfo{ID: providers.CapRunnerWindows, Support: providers.Native,
			Note: "agent label 'windows'; scripts use the pwsh step (PowerShell plugin)"},
		providers.CapabilityInfo{ID: providers.CapRunnerMacOS, Support: providers.Native, Note: "agent label 'macos'"},
		providers.CapabilityInfo{ID: providers.CapSecrets, Support: providers.Native,
			Note: "credentials('NAME'); create secret text credentials whose IDs match the secret names"},
		providers.CapabilityInfo{ID: providers.CapCheckout, Support: providers.Emulated,
			Note: "default checkout at the start of the stage; jobs without checkout skip it"},
		providers.CapabilityInfo{ID: providers.CapReusableSteps, Support: providers.Unsupported,
			Note: "Renvoy does not invent plugin steps; use run steps"},
		providers.CapabilityInfo{ID: providers.CapCache, Support: providers.Unsupported,
			Note: "Jenkins core has no cache step; remove the cache step"},
		providers.CapabilityInfo{ID: providers.CapArtifactUpload, Support: providers.Emulated,
			Note: "archiveArtifacts plus stash for downstream jobs"},
		providers.CapabilityInfo{ID: providers.CapArtifactDownload, Support: providers.Emulated, Note: "unstash"},
		providers.CapabilityInfo{ID: providers.CapTestResults, Support: providers.Native, Note: "post { always { junit } }"},
		providers.CapabilityInfo{ID: providers.CapImageBuild, Support: providers.Emulated, Note: "runs the docker CLI"},
		providers.CapabilityInfo{ID: providers.CapImagePush, Support: providers.Emulated, Note: "runs the docker CLI"},
		providers.CapabilityInfo{ID: providers.CapKubernetesDeploy, Support: providers.Emulated, Note: "runs kubectl apply"},
		providers.CapabilityInfo{ID: providers.CapEnvironments, Support: providers.Emulated,
			Note: "Jenkins has no deployment environments; the name is exported as RENVOY_ENVIRONMENT"},
		providers.CapabilityInfo{ID: providers.CapApprovals, Support: providers.Emulated,
			Note: "requires extensions.jenkins.input on the job, rendered as a stage input"},
	)
}

type extension struct {
	AgentLabel  string            `yaml:"agentLabel"`
	AgentLabels map[string]string `yaml:"agentLabels"`
}

type input struct {
	Message   string `yaml:"message"`
	OK        string `yaml:"ok"`
	Submitter string `yaml:"submitter"`
}

type jobExtension struct {
	AgentLabel string `yaml:"agentLabel"`
	Input      *input `yaml:"input"`
}

type config struct {
	ext  extension
	jobs map[*pipeline.Job]jobExtension
}

var (
	labelRe     = regexp.MustCompile(`^[A-Za-z0-9 ._&|()!-]+$`)
	submitterRe = regexp.MustCompile(`^[A-Za-z0-9._@-]+(?:,[A-Za-z0-9._@-]+)*$`)
)

func decode(p *pipeline.Pipeline) (config, diagnostics.List) {
	var diags diagnostics.List
	cfg := config{jobs: map[*pipeline.Job]jobExtension{}}
	if n := p.Extensions.Get(Name); n != nil {
		path := pipeline.JoinKey("extensions", Name)
		diags = append(diags, pipeline.DecodeStrict(n, &cfg.ext, path, nil)...)
		if cfg.ext.AgentLabel != "" && !validLabel(cfg.ext.AgentLabel) {
			diags = append(diags, diagnostics.Errorf(path+".agentLabel", "invalid agent label expression %q", cfg.ext.AgentLabel))
		}
		for _, os := range sortedKeys(cfg.ext.AgentLabels) {
			lp := pipeline.JoinKey(path+".agentLabels", os)
			if os != pipeline.OSLinux && os != pipeline.OSWindows && os != pipeline.OSMacOS {
				diags = append(diags, diagnostics.Errorf(lp, "unknown runner %q", os).WithHint("use linux, windows, or macos"))
			}
			if !validLabel(cfg.ext.AgentLabels[os]) {
				diags = append(diags, diagnostics.Errorf(lp, "invalid agent label expression %q", cfg.ext.AgentLabels[os]))
			}
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
		if je.AgentLabel != "" && !validLabel(je.AgentLabel) {
			diags = append(diags, diagnostics.Errorf(path+".agentLabel", "invalid agent label expression %q", je.AgentLabel))
		}
		if in := je.Input; in != nil {
			if in.Message == "" {
				diags = append(diags, diagnostics.Errorf(path+".input.message", "an input message is required"))
			}
			if strings.ContainsAny(in.Message+in.OK, "\n\r") {
				diags = append(diags, diagnostics.Errorf(path+".input", "input message and ok must be single lines"))
			}
			if in.Submitter != "" && !submitterRe.MatchString(in.Submitter) {
				diags = append(diags, diagnostics.Errorf(path+".input.submitter", "invalid submitter list %q", in.Submitter))
			}
		}
		cfg.jobs[j] = je
	}
	for i := range diags {
		diags[i].Provider = Name
	}
	return cfg, diags
}

// Validate implements providers.Provider.
func (pr *Provider) Validate(p *pipeline.Pipeline) diagnostics.List {
	diags := providers.CheckCapabilities(p, pr)
	cfg, extDiags := decode(p)
	diags = append(diags, extDiags...)
	add := func(d diagnostics.Diagnostic) { diags = append(diags, d.WithProvider(Name)) }

	for i, sc := range p.Triggers.Schedules {
		// Jenkins fires only when both day fields match; cron fires when either does.
		if f := strings.Fields(sc.Cron); len(f) == 5 && f[2] != "*" && f[4] != "*" {
			add(diagnostics.Errorf(pipeline.JoinIndex("triggers.schedules", i)+".cron",
				"Jenkins runs %q only on days matching both the day of month and the day of week, where cron runs on days matching either", sc.Cron).
				WithHint("restrict either the day of month or the day of week, and use a second schedule for the other"))
		}
	}
	if len(p.Secrets) > 0 {
		add(diagnostics.Infof("secrets", "create Jenkins secret text credentials with the IDs %s", strings.Join(p.Secrets, ", ")))
	}
	reserved := func(path, kind, name string) {
		if groovyReserved[name] {
			add(diagnostics.Errorf(path, "%s %q cannot be a Jenkins environment variable: Groovy reserves the name", kind, name).
				WithHint("rename it; Groovy keywords and default-imported class names such as URL or UUID cannot be assigned in an environment block"))
		}
	}
	for _, v := range p.Variables {
		reserved(pipeline.JoinKey("variables", v.Name), "variable", v.Name)
	}
	usedSecrets := map[string]bool{}
	for _, j := range p.AllJobs() {
		for _, e := range j.Env {
			reserved(pipeline.JoinKey(j.Path()+".env", e.Name), "env name", e.Name)
		}
		for _, name := range secretRefs(j) {
			usedSecrets[name] = true
		}
	}
	for i, name := range p.Secrets {
		if usedSecrets[name] {
			reserved(pipeline.JoinIndex("secrets", i), "secret", name)
		}
	}
	if !linear(p) {
		add(diagnostics.Infof("stages", "Jenkins runs these stages one after another (%s), so independent stages do not run concurrently",
			strings.Join(stageNames(p.Order), ", ")))
	}
	for _, s := range p.Stages {
		if layout(s) == sequential && len(s.Jobs) > 1 {
			add(diagnostics.Infof(s.Path()+".jobs", "Jenkins runs the jobs of stage %q one after another because some depend on others or use a matrix", s.Name))
		}
	}
	for _, j := range p.AllJobs() {
		jp := j.Path()
		je := cfg.jobs[j]
		if j.Environment != nil {
			if j.Environment.RequiresApproval() && je.Input == nil {
				add(diagnostics.Errorf(jp+".environment.approvals",
					"Jenkins Declarative Pipeline cannot express provider-managed environment approvals").
					WithHint("set extensions.jenkins.input on this job to pause for approval, or remove the portable approval requirement"))
			}
			add(diagnostics.Warningf(jp+".environment", "Jenkins has no deployment environments; %q is only exported as RENVOY_ENVIRONMENT", j.Environment.Name))
		}
		checkout := false
		for i, s := range j.Steps {
			sp := s.Path()
			if s.Kind() == pipeline.StepCheckout {
				checkout = true
				if i > 0 {
					add(diagnostics.Warningf(sp, "Jenkins checks out the repository when the stage starts, not at this position"))
				}
			}
			for _, e := range s.Env {
				for _, ref := range pipeline.MustTemplate(e.Value).Refs() {
					if ref.Namespace == pipeline.NSSecrets {
						add(diagnostics.Errorf(pipeline.JoinKey(sp+".env", e.Name),
							"Jenkins step environment cannot bind %s without interpolating the secret into Groovy", ref).
							WithHint("reference the secret directly in the script, or bind it in the job env as the whole value"))
					}
				}
			}
			if shell := s.ShellFor(j.OS()); s.Kind() == pipeline.StepRun && shell != pipeline.ShellPwsh && j.OS() == pipeline.OSWindows {
				add(diagnostics.Errorf(sp+".shell", "Jenkins on Windows runs scripts with pwsh; %s is not available", shell))
			}
		}
		for _, e := range j.Env {
			t := pipeline.MustTemplate(e.Value)
			for _, ref := range t.Refs() {
				if ref.Namespace == pipeline.NSSecrets && (len(t) != 1) {
					add(diagnostics.Errorf(pipeline.JoinKey(jp+".env", e.Name),
						"Jenkins can bind %s only when it is the entire value", ref).
						WithHint("set the variable to exactly %s", ref))
				}
			}
		}
		if !checkout && usesSHA(j) {
			add(diagnostics.Errorf(jp, "${git.sha} needs a checkout step in job %q on Jenkins, which sets GIT_COMMIT during checkout", j.Name))
		}
	}
	return diags
}

func usesSHA(j *pipeline.Job) bool {
	var texts []string
	for _, e := range j.Env {
		texts = append(texts, e.Value)
	}
	for _, s := range j.Steps {
		for _, e := range s.Env {
			texts = append(texts, e.Value)
		}
		for _, ref := range shared.CommandRefs(s) {
			if ref == (pipeline.Ref{Namespace: pipeline.NSGit, Name: pipeline.GitSHA}) {
				return true
			}
		}
	}
	return slices.Contains(shared.TextRefs(texts...), pipeline.Ref{Namespace: pipeline.NSGit, Name: pipeline.GitSHA})
}

func stageNames(stages []*pipeline.Stage) []string {
	var out []string
	for _, s := range stages {
		out = append(out, s.Name)
	}
	return out
}

// linear reports whether running stages in order adds no ordering the graph lacks.
func linear(p *pipeline.Pipeline) bool {
	for i, s := range p.Order {
		if i == 0 {
			if len(s.Needs) != 0 {
				return false
			}
			continue
		}
		if len(s.Needs) != 1 || s.Needs[0] != p.Order[i-1] {
			return false
		}
	}
	return true
}

type stageLayout int

const (
	single stageLayout = iota
	parallel
	sequential
)

func layout(s *pipeline.Stage) stageLayout {
	if len(s.Jobs) == 1 {
		return single
	}
	for _, j := range s.Jobs {
		if len(j.SameStageNeeds) > 0 || len(j.Matrix) > 0 {
			return sequential
		}
	}
	return parallel
}

// Render implements providers.Provider.
func (pr *Provider) Render(_ context.Context, p *pipeline.Pipeline, _ providers.RenderOptions) ([]providers.GeneratedFile, error) {
	cfg, diags := decode(p)
	if diags.HasErrors() {
		return nil, fmt.Errorf("jenkins: invalid extensions")
	}
	r := &renderer{p: p, cfg: cfg}
	w := &writer{}
	w.open("pipeline")
	w.line("agent none")
	if crons := cronExpr(p.Triggers.Schedules); crons != "" {
		w.open("triggers")
		w.line("cron(%s)", crons)
		w.close()
	}
	env := map[string]string{}
	var envOrder []string
	for _, v := range p.Variables {
		envOrder = append(envOrder, v.Name)
		env[v.Name] = sq(v.Value)
	}
	if r.usesBranch() {
		envOrder = append(envOrder, branchVar)
		env[branchVar] = `"${env.CHANGE_BRANCH ?: env.BRANCH_NAME}"`
	}
	if len(envOrder) > 0 {
		w.open("environment")
		for _, k := range envOrder {
			w.line("%s = %s", k, env[k])
		}
		w.close()
	}
	w.open("stages")
	for _, s := range p.Order {
		r.stage(w, s)
	}
	w.close()
	w.blank()
	w.raw(render.CustomBlock(providers.CommentSlash, "    "))
	w.close()
	return []providers.GeneratedFile{{Path: "Jenkinsfile", Content: []byte(w.String()), Comment: providers.CommentSlash}}, nil
}

// cronExpr renders schedules as one Jenkins cron spec chosen by branch.
func cronExpr(schedules []pipeline.Schedule) string {
	if len(schedules) == 0 {
		return ""
	}
	byBranch := map[string][]string{}
	var branches []string
	for _, s := range schedules {
		for _, b := range s.Branches {
			if _, ok := byBranch[b]; !ok {
				branches = append(branches, b)
			}
			byBranch[b] = append(byBranch[b], s.Cron)
		}
	}
	// a ? x : b ? y : '' is right-associative, so the chain is written in order.
	var b strings.Builder
	for _, branch := range branches {
		spec := "TZ=UTC\n" + strings.Join(byBranch[branch], "\n")
		fmt.Fprintf(&b, "env.BRANCH_NAME == %s ? %s : ", sq(branch), sq(spec))
	}
	b.WriteString("''")
	return b.String()
}

type renderer struct {
	p   *pipeline.Pipeline
	cfg config
}

func (r *renderer) usesBranch() bool {
	for _, j := range r.p.AllJobs() {
		var texts []string
		for _, e := range j.Env {
			texts = append(texts, e.Value)
		}
		for _, s := range j.Steps {
			for _, e := range s.Env {
				texts = append(texts, e.Value)
			}
			if slices.Contains(shared.CommandRefs(s), pipeline.Ref{Namespace: pipeline.NSGit, Name: pipeline.GitBranch}) {
				return true
			}
		}
		if slices.Contains(shared.TextRefs(texts...), pipeline.Ref{Namespace: pipeline.NSGit, Name: pipeline.GitBranch}) {
			return true
		}
	}
	return false
}

func (r *renderer) stage(w *writer, s *pipeline.Stage) {
	w.open("stage(%s)", sq(s.Name))
	switch layout(s) {
	case parallel:
		w.open("parallel")
	default:
		w.open("stages")
	}
	for _, j := range s.JobOrder {
		r.job(w, j)
	}
	w.close()
	w.close()
}

func (r *renderer) label(j *pipeline.Job) string {
	if je := r.cfg.jobs[j]; je.AgentLabel != "" {
		return je.AgentLabel
	}
	if l, ok := r.cfg.ext.AgentLabels[j.OS()]; ok {
		return l
	}
	if r.cfg.ext.AgentLabel != "" && j.OS() == pipeline.OSLinux {
		return r.cfg.ext.AgentLabel
	}
	return j.OS()
}

func (r *renderer) agent(w *writer, j *pipeline.Job) {
	w.open("agent")
	if c := j.Container; c != nil {
		w.open("docker")
		w.line("image %s", r.groovyValue(c.Image))
		w.line("label %s", sq(r.label(j)))
		if c.Options != "" {
			w.line("args %s", sq(c.Options))
		}
		w.close()
	} else {
		w.line("label %s", sq(r.label(j)))
	}
	w.close()
}

// groovyValue renders a template as a Groovy string. Variables are inlined;
// matrix and git references read the environment.
func (r *renderer) groovyValue(value string) string {
	t := pipeline.MustTemplate(value)
	if t.IsLiteral() {
		return sq(t.Literal())
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, seg := range t {
		switch {
		case seg.Ref == nil:
			b.WriteString(escapeDouble(seg.Literal))
		case seg.Ref.Namespace == pipeline.NSVariables:
			v, _ := r.p.Variables.Get(seg.Ref.Name)
			b.WriteString(escapeDouble(v))
		case seg.Ref.Namespace == pipeline.NSMatrix:
			b.WriteString("${" + seg.Ref.Name + "}")
		default:
			b.WriteString("${env." + envName(*seg.Ref) + "}")
		}
	}
	b.WriteByte('"')
	return b.String()
}

func envName(ref pipeline.Ref) string {
	if ref.Namespace == pipeline.NSGit {
		if ref.Name == pipeline.GitBranch {
			return branchVar
		}
		return "GIT_COMMIT"
	}
	return ref.Name
}

func (r *renderer) job(w *writer, j *pipeline.Job) {
	if len(j.Matrix) > 0 {
		w.open("stage(%s)", sq(j.Name))
		w.open("matrix")
		w.open("axes")
		for _, a := range j.Matrix {
			w.open("axis")
			w.line("name %s", sq(a.Name))
			vals := make([]string, len(a.Values))
			for i, v := range a.Values {
				vals[i] = sq(v)
			}
			w.line("values %s", strings.Join(vals, ", "))
			w.close()
		}
		w.close()
		r.directives(w, j)
		w.open("stages")
		w.open("stage(%s)", sq(j.Name+" / matrix"))
		r.body(w, j)
		w.close()
		w.close()
		w.close()
		w.close()
		return
	}
	w.open("stage(%s)", sq(j.Name))
	r.directives(w, j)
	r.body(w, j)
	w.close()
}

// directives writes agent, when, options, input, and environment for a job.
func (r *renderer) directives(w *writer, j *pipeline.Job) {
	r.agent(w, j)
	if conds := shared.InheritedConditions(r.p, j); len(conds) > 0 {
		w.open("when")
		w.line("beforeAgent true")
		if r.cfg.jobs[j].Input != nil {
			w.line("beforeInput true")
		}
		for _, c := range conds {
			writeCondition(w, c)
		}
		w.close()
	}
	var opts []string
	if !hasCheckout(j) {
		opts = append(opts, "skipDefaultCheckout()")
	}
	if j.TimeoutMinutes > 0 {
		opts = append(opts, fmt.Sprintf("timeout(time: %d, unit: 'MINUTES')", j.TimeoutMinutes))
	}
	if j.Retries > 0 {
		opts = append(opts, fmt.Sprintf("retry(%d)", j.Retries+1))
	}
	if len(opts) > 0 {
		w.open("options")
		for _, o := range opts {
			w.line("%s", o)
		}
		w.close()
	}
	if in := r.cfg.jobs[j].Input; in != nil {
		w.open("input")
		w.line("message %s", sq(in.Message))
		if in.OK != "" {
			w.line("ok %s", sq(in.OK))
		}
		if in.Submitter != "" {
			w.line("submitter %s", sq(in.Submitter))
		}
		w.close()
	}
	var env [][2]string
	if j.Environment != nil {
		env = append(env, [2]string{"RENVOY_ENVIRONMENT", sq(j.Environment.Name)})
	}
	for _, e := range j.Env {
		t := pipeline.MustTemplate(e.Value)
		if len(t) == 1 && t[0].Ref != nil && t[0].Ref.Namespace == pipeline.NSSecrets {
			env = append(env, [2]string{e.Name, "credentials(" + sq(t[0].Ref.Name) + ")"})
		} else {
			env = append(env, [2]string{e.Name, r.groovyValue(e.Value)})
		}
	}
	for _, name := range secretRefs(j) {
		env = append(env, [2]string{name, "credentials(" + sq(name) + ")"})
	}
	if len(env) > 0 {
		w.open("environment")
		for _, e := range env {
			w.line("%s = %s", e[0], e[1])
		}
		w.close()
	}
}

// secretRefs lists secrets referenced by the job's commands, in first-use order.
func secretRefs(j *pipeline.Job) []string {
	var out []string
	for _, s := range j.Steps {
		for _, ref := range shared.CommandRefs(s) {
			if ref.Namespace == pipeline.NSSecrets && !slices.Contains(out, ref.Name) {
				out = append(out, ref.Name)
			}
		}
	}
	return out
}

func hasCheckout(j *pipeline.Job) bool {
	for _, s := range j.Steps {
		if s.Kind() == pipeline.StepCheckout {
			return true
		}
	}
	return false
}

func writeCondition(w *writer, c *pipeline.Condition) {
	if len(c.Branches) > 0 {
		var alts []string
		for _, b := range c.Branches {
			if lit, prefix := pipeline.PatternPrefix(b); prefix {
				alts = append(alts, fmt.Sprintf("branch pattern: %s, comparator: 'REGEXP'", sq(regexp.QuoteMeta(lit)+".*")))
			} else {
				alts = append(alts, "branch "+sq(b))
			}
		}
		writeAnyOf(w, alts)
	}
	if len(c.Events) > 0 {
		var alts []string
		for _, e := range c.Events {
			switch e {
			case pipeline.EventPullRequest:
				alts = append(alts, "changeRequest()")
			case pipeline.EventManual:
				alts = append(alts, "triggeredBy cause: 'UserIdCause'")
			case pipeline.EventSchedule:
				alts = append(alts, "triggeredBy 'TimerTrigger'")
			case pipeline.EventPush:
				alts = append(alts, "not { anyOf { changeRequest(); triggeredBy cause: 'UserIdCause'; triggeredBy 'TimerTrigger' } }")
			}
		}
		writeAnyOf(w, alts)
	}
}

func writeAnyOf(w *writer, alts []string) {
	if len(alts) == 1 {
		w.line("%s", alts[0])
		return
	}
	w.open("anyOf")
	for _, a := range alts {
		w.line("%s", a)
	}
	w.close()
}

func (r *renderer) body(w *writer, j *pipeline.Job) {
	var junit []string
	w.open("steps")
	wrote := false
	for _, s := range j.Steps {
		switch s.Kind() {
		case pipeline.StepCheckout:
			continue
		case pipeline.StepPublishTestResults:
			junit = append(junit, s.PublishTestResults.Path)
			continue
		case pipeline.StepUploadArtifact:
			includes := antPatterns(s.UploadArtifact.Paths)
			w.line("archiveArtifacts artifacts: %s", sq(includes))
			w.line("stash name: %s, includes: %s", sq(s.UploadArtifact.Name), sq(includes))
		case pipeline.StepDownloadArtifact:
			w.line("unstash %s", sq(s.DownloadArtifact.Name))
		default:
			r.command(w, j, s)
		}
		wrote = true
	}
	if !wrote {
		w.line("echo 'No steps to run'")
	}
	w.close()
	if len(junit) > 0 {
		w.open("post")
		w.open("always")
		for _, p := range junit {
			w.line("junit allowEmptyResults: true, testResults: %s", sq(p))
		}
		w.close()
		w.close()
	}
}

// antPatterns converts artifact paths to a comma-separated Ant include list.
func antPatterns(paths []string) string {
	out := make([]string, len(paths))
	for i, p := range paths {
		if strings.HasSuffix(p, "/") {
			out[i] = p + "**"
		} else {
			out[i] = p
		}
	}
	return strings.Join(out, ",")
}

func (r *renderer) command(w *writer, j *pipeline.Job, s *pipeline.Step) {
	shell := s.ShellFor(j.OS())
	script := strings.TrimRight(shared.Command(s, shell, envName), "\n")
	step := "sh"
	switch shell {
	case pipeline.ShellPwsh:
		step = "pwsh"
	case pipeline.ShellBash:
		script = "#!/bin/bash\nset -eo pipefail\n" + script
	}
	var wrappers int
	if s.WorkingDirectory != "" {
		w.open("dir(%s)", sq(s.WorkingDirectory))
		wrappers++
	}
	if len(s.Env) > 0 {
		var pairs []string
		for _, e := range s.Env {
			pairs = append(pairs, r.groovyEnvPair(e))
		}
		w.open("withEnv([%s])", strings.Join(pairs, ", "))
		wrappers++
	}
	label := ""
	if name := shared.DisplayName(s); name != "" {
		label = "label: " + sq(name) + ", "
	}
	if strings.Contains(script, "\n") {
		w.rawLine(fmt.Sprintf("%s %sscript: '''%s'''", step, label, escapeSingle(script)))
	} else {
		w.line("%s %sscript: %s", step, label, sq(script))
	}
	for ; wrappers > 0; wrappers-- {
		w.close()
	}
}

func (r *renderer) groovyEnvPair(e pipeline.Var) string {
	if t := pipeline.MustTemplate(e.Value); t.IsLiteral() {
		return sq(e.Name + "=" + t.Literal())
	}
	return `"` + e.Name + "=" + r.groovyValue(e.Value)[1:]
}

func escapeSingle(s string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s)
}

func escapeDouble(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "\n", `\n`, "\r", `\r`).Replace(s)
}

// sq renders a single-quoted Groovy string, which performs no interpolation.
func sq(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`, "\r", `\r`).Replace(s) + "'"
}

type writer struct {
	b      strings.Builder
	indent int
}

func (w *writer) line(format string, args ...any) {
	w.b.WriteString(strings.Repeat("    ", w.indent))
	fmt.Fprintf(&w.b, format, args...)
	w.b.WriteByte('\n')
}

// rawLine writes an indented line whose content may span lines; continuation
// lines are not indented so multi-line strings keep their exact text.
func (w *writer) rawLine(s string) {
	w.b.WriteString(strings.Repeat("    ", w.indent))
	w.b.WriteString(s)
	w.b.WriteByte('\n')
}

func (w *writer) raw(s string) { w.b.WriteString(s) }

func (w *writer) blank() { w.b.WriteByte('\n') }

func (w *writer) open(format string, args ...any) {
	w.line(format+" {", args...)
	w.indent++
}

func (w *writer) close() {
	w.indent--
	w.line("}")
}

func (w *writer) String() string { return w.b.String() }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// validLabel reports whether s is a Jenkins label expression Renvoy can emit:
// labels joined by &&, ||, !, and parentheses. Jenkins parses anything else
// into a label no agent has, so the build would wait forever.
func validLabel(s string) bool {
	if !labelRe.MatchString(s) {
		return false
	}
	var toks []string
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == ' ':
			i++
		case strings.HasPrefix(s[i:], "&&"), strings.HasPrefix(s[i:], "||"):
			toks = append(toks, s[i:i+2])
			i += 2
		case c == '!' || c == '(' || c == ')':
			toks = append(toks, s[i:i+1])
			i++
		case c == '&' || c == '|':
			return false
		default:
			if !isAlnum(c) {
				return false
			}
			j := i
			for j < len(s) && strings.IndexByte(" &|!()", s[j]) < 0 {
				j++
			}
			toks = append(toks, "a")
			i = j
		}
	}
	pos := 0
	var or, and, unary func() bool
	or = func() bool {
		if !and() {
			return false
		}
		for pos < len(toks) && toks[pos] == "||" {
			pos++
			if !and() {
				return false
			}
		}
		return true
	}
	and = func() bool {
		if !unary() {
			return false
		}
		for pos < len(toks) && toks[pos] == "&&" {
			pos++
			if !unary() {
				return false
			}
		}
		return true
	}
	unary = func() bool {
		if pos >= len(toks) {
			return false
		}
		switch toks[pos] {
		case "!":
			pos++
			return unary()
		case "(":
			pos++
			if !or() || pos >= len(toks) || toks[pos] != ")" {
				return false
			}
			pos++
			return true
		case "a":
			pos++
			return true
		}
		return false
	}
	return or() && pos == len(toks)
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
