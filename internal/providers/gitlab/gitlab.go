// Package gitlab renders pipelines as GitLab CI configuration.
package gitlab

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/amitpatole/renvoy/internal/compiler"
	"github.com/amitpatole/renvoy/internal/diagnostics"
	"github.com/amitpatole/renvoy/internal/pipeline"
	"github.com/amitpatole/renvoy/internal/providers"
	"github.com/amitpatole/renvoy/internal/providers/shared"
	"github.com/amitpatole/renvoy/internal/render"
)

// Name is the provider identifier and extension namespace.
const Name = "gitlab"

// Provider renders .gitlab-ci.yml.
type Provider struct{}

// New returns the GitLab provider.
func New() *Provider { return &Provider{} }

// Name implements providers.Provider.
func (*Provider) Name() string { return Name }

// DisplayName implements providers.Provider.
func (*Provider) DisplayName() string { return "GitLab CI" }

// Description implements providers.Provider.
func (*Provider) Description() string { return "GitLab CI/CD pipeline (.gitlab-ci.yml)" }

// Capabilities implements providers.Provider.
func (*Provider) Capabilities() providers.CapabilitySet {
	return providers.NewCapabilitySet(
		providers.CapabilityInfo{ID: providers.CapScheduleTrigger, Support: providers.ProviderManaged,
			Note: "create a pipeline schedule (timezone UTC) for each listed branch under Build > Pipeline schedules; the generated workflow rules accept scheduled pipelines"},
		providers.CapabilityInfo{ID: providers.CapStepConditions, Support: providers.Unsupported,
			Note: "GitLab has no step-level conditions; move the step into a job with a when condition"},
		providers.CapabilityInfo{ID: providers.CapContainerOptions, Support: providers.Unsupported,
			Note: "GitLab cannot pass docker run options from the pipeline; configure them on the runner"},
		providers.CapabilityInfo{ID: providers.CapReusableSteps, Support: providers.Unsupported,
			Note: "GitLab has no reusable step references; use run steps"},
		providers.CapabilityInfo{ID: providers.CapCheckout, Support: providers.Emulated,
			Note: "GitLab always clones before the script; jobs without checkout set GIT_STRATEGY: none"},
		providers.CapabilityInfo{ID: providers.CapStepEnv, Support: providers.Emulated,
			Note: "rendered as a subshell that exports the variables (bash and sh only)"},
		providers.CapabilityInfo{ID: providers.CapWorkingDirectory, Support: providers.Emulated,
			Note: "rendered as a subshell that changes directory (bash and sh only)"},
		providers.CapabilityInfo{ID: providers.CapCache, Support: providers.Emulated,
			Note: "job-level cache, restored before the script and saved when the job succeeds"},
		providers.CapabilityInfo{ID: providers.CapArtifactUpload, Support: providers.Emulated,
			Note: "job artifacts, uploaded after the script finishes"},
		providers.CapabilityInfo{ID: providers.CapArtifactDownload, Support: providers.Emulated,
			Note: "downloaded through needs with artifacts: true"},
		providers.CapabilityInfo{ID: providers.CapTestResults, Support: providers.Native, Note: "artifacts:reports:junit, uploaded even when the job fails"},
		providers.CapabilityInfo{ID: providers.CapSecrets, Support: providers.Native,
			Note: "create masked CI/CD variables with the secret names; GitLab masks only values of at least 8 characters on one line, with no spaces and only letters, digits, and _ : @ - + . ~ = /; protected variables are empty on unprotected branches"},
		providers.CapabilityInfo{ID: providers.CapImageBuild, Support: providers.Emulated, Note: "runs the docker CLI"},
		providers.CapabilityInfo{ID: providers.CapImagePush, Support: providers.Emulated, Note: "runs the docker CLI"},
		providers.CapabilityInfo{ID: providers.CapKubernetesDeploy, Support: providers.Emulated, Note: "runs kubectl apply"},
		providers.CapabilityInfo{ID: providers.CapRunnerWindows, Support: providers.Native,
			Note: "defaults to GitLab-hosted runner tags; override with extensions.gitlab.runnerTags"},
		providers.CapabilityInfo{ID: providers.CapRunnerMacOS, Support: providers.Native,
			Note: "defaults to GitLab-hosted runner tags; override with extensions.gitlab.runnerTags"},
		providers.CapabilityInfo{ID: providers.CapRetries, Support: providers.Native, Note: "GitLab retries a job at most twice"},
		providers.CapabilityInfo{ID: providers.CapApprovals, Support: providers.ProviderManaged,
			Note: "protect the environment and require approvals under Operate > Environments"},
	)
}

type extension struct {
	RunnerTags map[string][]string `yaml:"runnerTags"`
}

type jobExtension struct {
	Tags []string `yaml:"tags"`
}

type config struct {
	ext  extension
	jobs map[*pipeline.Job]jobExtension
}

var (
	tagRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	// reservedKeys are top-level .gitlab-ci.yml keywords that cannot be job names.
	reservedKeys = []string{"image", "services", "stages", "types", "before_script", "after_script",
		"variables", "cache", "include", "workflow", "default", "pages", "spec"}
	// globalKeys apply to every job, so custom blocks may not add them. A custom
	// "pages" job is fine: it is a job, not a global setting.
	globalKeys = []string{"image", "services", "stages", "types", "before_script", "after_script",
		"variables", "cache", "include", "workflow", "default", "spec"}
	defaultTags = map[string][]string{
		pipeline.OSWindows: {"saas-windows-medium-amd64"},
		pipeline.OSMacOS:   {"saas-macos-medium-m1"},
	}
)

// jobKey is the GitLab job name. Reserved keywords are prefixed with the stage;
// the colon cannot appear in Renvoy names, so keys stay unique.
func jobKey(j *pipeline.Job) string {
	if slices.Contains(reservedKeys, j.Name) {
		return j.Stage.Name + ":" + j.Name
	}
	return j.Name
}

func decode(p *pipeline.Pipeline) (config, diagnostics.List) {
	var diags diagnostics.List
	cfg := config{jobs: map[*pipeline.Job]jobExtension{}}
	if n := p.Extensions.Get(Name); n != nil {
		path := pipeline.JoinKey("extensions", Name)
		diags = append(diags, pipeline.DecodeStrict(n, &cfg.ext, path, nil)...)
		for _, os := range sortedKeys(cfg.ext.RunnerTags) {
			tags := cfg.ext.RunnerTags[os]
			tp := pipeline.JoinKey(path+".runnerTags", os)
			if os != pipeline.OSLinux && os != pipeline.OSWindows && os != pipeline.OSMacOS {
				diags = append(diags, diagnostics.Errorf(tp, "unknown runner %q", os).WithHint("use linux, windows, or macos"))
			}
			diags = append(diags, checkTags(tp, tags)...)
		}
	}
	for _, j := range p.AllJobs() {
		if n := j.Extensions.Get(Name); n != nil {
			path := pipeline.JoinKey(j.Path()+".extensions", Name)
			var je jobExtension
			diags = append(diags, pipeline.DecodeStrict(n, &je, path, nil)...)
			diags = append(diags, checkTags(path+".tags", je.Tags)...)
			cfg.jobs[j] = je
		}
	}
	for i := range diags {
		diags[i].Provider = Name
	}
	return cfg, diags
}

func checkTags(path string, tags []string) diagnostics.List {
	var diags diagnostics.List
	for i, t := range tags {
		if !tagRe.MatchString(t) {
			diags = append(diags, diagnostics.Errorf(pipeline.JoinIndex(path, i), "invalid runner tag %q", t))
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
	diags = append(diags, shared.GitVarCollisions(p, Name, gitVar)...)

	// GitLab's predefined variables use these prefixes; some cannot be overridden.
	// YAML variables (pipeline variables, job env, and matrix axes) also reach
	// the rules Renvoy generates and the runner's clone settings; step env is
	// exported inside the script and reaches neither.
	predefined := func(path, name string, yamlVar bool) {
		up := strings.ToUpper(name)
		switch {
		case gitVar(up):
			// Reported by GitVarCollisions.
		case yamlVar && ruleVar(up):
			add(diagnostics.Errorf(path, "%q would replace the GitLab variable that the generated rules read", name).
				WithHint("rename it; GitLab lets pipeline variables override predefined ones such as CI_COMMIT_BRANCH"))
		case yamlVar && (up == "GIT_STRATEGY" || up == "GIT_CHECKOUT"):
			add(diagnostics.Errorf(path, "%s is set by Renvoy from the job's checkout steps", name).
				WithHint("add or remove the checkout step instead"))
		case yamlVar && strings.HasPrefix(up, "GIT_"):
			add(diagnostics.Warningf(path, "%q may change how the GitLab runner clones the repository", name).
				WithHint("rename it unless you mean to change the clone; the runner reads GIT_DEPTH, GIT_CLEAN_FLAGS, and other GIT_ settings"))
		case strings.HasPrefix(up, "CI_") || strings.HasPrefix(up, "GITLAB_"):
			add(diagnostics.Warningf(path, "%q uses a prefix of GitLab's predefined variables, which it may ignore or that may change what GitLab does", name).
				WithHint("rename it; GitLab does not let pipelines override some predefined variables, such as CI_ENVIRONMENT_URL"))
		}
	}
	for _, v := range p.Variables {
		predefined(pipeline.JoinKey("variables", v.Name), v.Name, true)
	}
	// Secrets are project variables, which override predefined ones too; with a
	// predefined name the script could read GitLab's value instead.
	for i, s := range p.Secrets {
		if up := strings.ToUpper(s); strings.HasPrefix(up, "CI_") || strings.HasPrefix(up, "GITLAB_") {
			add(diagnostics.Errorf(pipeline.JoinIndex("secrets", i), "secret %q uses a prefix of GitLab's predefined variables, so scripts could read GitLab's value instead", s).
				WithHint("rename the secret and its CI/CD variable"))
			continue
		}
		predefined(pipeline.JoinIndex("secrets", i), s, true)
	}
	// GitLab expands $NAME, ${NAME}, and %NAME% in variables; Renvoy escapes $,
	// but %NAME% has no escape.
	percent := func(path string, vars pipeline.Vars) {
		for _, e := range vars {
			if percentVarRe.MatchString(e.Value) {
				add(diagnostics.Errorf(pipeline.JoinKey(path, e.Name), "GitLab would expand %q in the value of %s", percentVarRe.FindString(e.Value), e.Name).
					WithHint("set the value inside the script, where GitLab does not expand it"))
			}
		}
	}
	percent("variables", p.Variables)
	for _, j := range p.AllJobs() {
		if j.Environment != nil && percentVarRe.MatchString(j.Environment.URL) {
			add(diagnostics.Errorf(j.Path()+".environment.url", "GitLab would expand %q in the environment URL", percentVarRe.FindString(j.Environment.URL)).
				WithHint("GitLab expands $NAME, ${NAME}, and %%NAME%% in environment URLs; write the URL without %%NAME%% sequences"))
		}
		percent(j.Path()+".env", j.Env)
		for _, axis := range j.Matrix {
			for i, val := range axis.Values {
				if percentVarRe.MatchString(val) {
					add(diagnostics.Errorf(pipeline.JoinIndex(pipeline.JoinKey(j.Path()+".matrix", axis.Name), i), "GitLab would expand %q in matrix value %q", percentVarRe.FindString(val), val))
				}
			}
		}
	}
	for _, j := range p.AllJobs() {
		for _, e := range j.Env {
			predefined(pipeline.JoinKey(j.Path()+".env", e.Name), e.Name, true)
		}
		for _, s := range j.Steps {
			for _, e := range s.Env {
				predefined(pipeline.JoinKey(s.Path()+".env", e.Name), e.Name, false)
			}
		}
		for _, axis := range j.Matrix {
			predefined(pipeline.JoinKey(j.Path()+".matrix", axis.Name), axis.Name, true)
		}
	}
	artifacts := compiler.Artifacts(p)
	for _, j := range p.AllJobs() {
		jp := j.Path()
		if n := len(needsOf(p, artifacts, j)); n > maxNeeds {
			add(diagnostics.Warningf(jp, "the job needs %d jobs; GitLab allows %d by default (ci_max_needs on self-managed instances)", n, maxNeeds).
				WithHint("reduce the jobs in the stages it depends on, or raise the limit on your instance"))
		}
		if n := len(j.Matrix.Combinations()); len(j.Matrix) > 0 && n > maxMatrixJobs {
			add(diagnostics.Errorf(jp+".matrix", "the matrix expands to %d jobs; GitLab parallel:matrix allows at most %d", n, maxMatrixJobs))
		}
		if slices.Contains(reservedKeys, j.Name) {
			add(diagnostics.Infof(jp+".name", "%q is a reserved GitLab CI keyword, so the job is named %q", j.Name, jobKey(j)))
		}
		if j.Retries > 2 {
			add(diagnostics.Errorf(jp+".retries", "GitLab retries a job at most 2 times"))
		}
		if _, custom := cfg.jobs[j]; !custom && j.OS() != pipeline.OSLinux && cfg.ext.RunnerTags[j.OS()] == nil {
			add(diagnostics.Infof(jp+".runsOn", "%s jobs use the GitLab-hosted runner tags %v", j.OS(), defaultTags[j.OS()]).
				WithHint("set extensions.gitlab.runnerTags.%s for self-managed runners", j.OS()))
		}
		var caches, uploads int
		lastUpload := -1
		for i, s := range j.Steps {
			sp := s.Path()
			switch s.Kind() {
			case pipeline.StepCheckout:
				if i > 0 {
					add(diagnostics.Warningf(sp, "GitLab clones the repository before the first step, not at this position"))
				}
			case pipeline.StepRun:
				if shell := s.ShellFor(j.OS()); (shell == pipeline.ShellPwsh) != (j.OS() == pipeline.OSWindows) {
					add(diagnostics.Errorf(sp+".shell", "GitLab runs every script in the runner's shell; %s is not available on %s runners", shell, j.OS()).
						WithHint("remove shell, or move the step into a job on a matching runner"))
				}
			case pipeline.StepCache:
				caches++
				if caches == 5 {
					add(diagnostics.Errorf(sp, "GitLab supports at most 4 caches per job"))
				}
				if len(s.Cache.Files) > 2 {
					add(diagnostics.Errorf(sp+".cache.files", "GitLab cache keys can hash at most 2 files"))
				}
			case pipeline.StepUploadArtifact:
				uploads++
				lastUpload = i
			}
			if (len(s.Env) > 0 || s.WorkingDirectory != "") && j.OS() == pipeline.OSWindows {
				add(diagnostics.Errorf(sp, "GitLab step env and workingDirectory are only supported on bash and sh runners"))
			}
		}
		if lastUpload >= 0 && lastUpload < len(j.Steps)-1 && hasLaterCommand(j.Steps[lastUpload+1:]) {
			add(diagnostics.Warningf(j.Steps[lastUpload].Path(),
				"GitLab uploads artifacts after the whole script, so the upload is skipped if a later step fails"))
		}
		if uploads > 0 && hasTestResults(j) {
			add(diagnostics.Warningf(jp, "GitLab uploads all job artifacts when the job fails because test results are published"))
		}
	}
	return diags
}

func hasLaterCommand(steps []*pipeline.Step) bool {
	for _, s := range steps {
		if shared.IsCommand(s) {
			return true
		}
	}
	return false
}

func hasTestResults(j *pipeline.Job) bool {
	for _, s := range j.Steps {
		if s.PublishTestResults != nil {
			return true
		}
	}
	return false
}

// Render implements providers.Provider.
func (pr *Provider) Render(_ context.Context, p *pipeline.Pipeline, _ providers.RenderOptions) ([]providers.GeneratedFile, error) {
	cfg, diags := decode(p)
	if diags.HasErrors() {
		return nil, fmt.Errorf("gitlab: invalid extensions")
	}
	r := &renderer{p: p, cfg: cfg, artifacts: compiler.Artifacts(p)}
	root := shared.NewMap()
	root.Set("workflow", shared.NewMap().Set("name", p.Metadata.Name).Set("rules", r.workflowRules()))
	var stages []string
	for _, s := range p.Order {
		stages = append(stages, s.Name)
	}
	root.Set("stages", stages)
	if len(p.Variables) > 0 {
		vars := shared.NewMap()
		for _, v := range p.Variables {
			vars.Set(v.Name, escapeDollar(v.Value))
		}
		root.Set("variables", vars)
	}
	for _, j := range p.OrderedJobs() {
		root.Set(jobKey(j), r.job(j))
	}
	body, err := shared.Encode(root)
	if err != nil {
		return nil, err
	}
	body = append(body, '\n')
	body = append(body, render.CustomBlock(providers.CommentHash, "")...)
	return []providers.GeneratedFile{{Path: ".gitlab-ci.yml", Content: body, Comment: providers.CommentHash, YAML: true,
		CustomKeys: providers.CustomKeyPolicy{Denied: globalKeys}}}, nil
}

type renderer struct {
	p         *pipeline.Pipeline
	cfg       config
	artifacts map[string]compiler.Artifact
}

// percentVarRe matches the %NAME% form GitLab expands in variables.
var percentVarRe = regexp.MustCompile(`%[A-Za-z_][A-Za-z0-9_]*%`)

// escapeDollar keeps literal "$" from being expanded in GitLab variables.
func escapeDollar(s string) string { return strings.ReplaceAll(s, "$", "$$") }

func regexLiteral(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`.+*?()[]{}|^$\/`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func match(variable, pat string) string {
	if lit, prefix := pipeline.PatternPrefix(pat); prefix {
		return fmt.Sprintf("%s =~ /^%s/", variable, regexLiteral(lit))
	}
	return fmt.Sprintf("%s == %q", variable, pat)
}

func anyOf(variable string, pats []string) string {
	var alts []string
	for _, p := range pats {
		alts = append(alts, match(variable, p))
	}
	return group(alts)
}

func group(alts []string) string {
	if len(alts) == 1 {
		return alts[0]
	}
	return "(" + strings.Join(alts, " || ") + ")"
}

func (r *renderer) workflowRules() []*shared.Map {
	t := r.p.Triggers
	var rules []string
	if t.Push != nil {
		const src = `$CI_PIPELINE_SOURCE == "push"`
		if len(t.Push.Branches) == 0 && len(t.Push.Tags) == 0 {
			rules = append(rules, src)
		}
		if len(t.Push.Branches) > 0 {
			rules = append(rules, src+" && "+anyOf("$CI_COMMIT_BRANCH", t.Push.Branches))
		}
		if len(t.Push.Tags) > 0 {
			rules = append(rules, src+" && "+anyOf("$CI_COMMIT_TAG", t.Push.Tags))
		}
	}
	if t.PullRequest != nil {
		rule := `$CI_PIPELINE_SOURCE == "merge_request_event"`
		if len(t.PullRequest.Branches) > 0 {
			rule += " && " + anyOf("$CI_MERGE_REQUEST_TARGET_BRANCH_NAME", t.PullRequest.Branches)
		}
		rules = append(rules, rule)
	}
	if t.Manual != nil {
		rules = append(rules, `$CI_PIPELINE_SOURCE == "web"`)
	}
	if len(t.Schedules) > 0 {
		rules = append(rules, `$CI_PIPELINE_SOURCE == "schedule"`)
	}
	var out []*shared.Map
	for _, rule := range rules {
		out = append(out, shared.NewMap().Set("if", rule))
	}
	return out
}

var eventSources = map[string]string{
	pipeline.EventPush:        "push",
	pipeline.EventPullRequest: "merge_request_event",
	pipeline.EventManual:      "web",
	pipeline.EventSchedule:    "schedule",
}

func condition(conds []*pipeline.Condition) string {
	var parts []string
	for _, c := range conds {
		if len(c.Branches) > 0 {
			parts = append(parts, anyOf("$CI_COMMIT_BRANCH", c.Branches))
		}
		if len(c.Events) > 0 {
			var alts []string
			for _, e := range c.Events {
				alts = append(alts, fmt.Sprintf(`$CI_PIPELINE_SOURCE == %q`, eventSources[e]))
			}
			parts = append(parts, group(alts))
		}
	}
	if len(parts) == 1 && strings.HasPrefix(parts[0], "(") {
		return parts[0][1 : len(parts[0])-1]
	}
	return strings.Join(parts, " && ")
}

func envName(ref pipeline.Ref) string {
	if ref.Namespace == pipeline.NSGit {
		if ref.Name == pipeline.GitBranch {
			return "CI_COMMIT_REF_NAME"
		}
		return "CI_COMMIT_SHA"
	}
	return ref.Name
}

// value renders a template for GitLab variable expansion (image, variables).
func value(t string) string {
	var b strings.Builder
	for _, seg := range pipeline.MustTemplate(t) {
		if seg.Ref != nil {
			b.WriteString("${" + envName(*seg.Ref) + "}")
		} else {
			b.WriteString(escapeDollar(seg.Literal))
		}
	}
	return b.String()
}

func producesArtifacts(j *pipeline.Job) bool {
	for _, s := range j.Steps {
		if s.UploadArtifact != nil || s.PublishTestResults != nil {
			return true
		}
	}
	return false
}

// GitLab's default limit on needs entries, and its hard limit on jobs from one
// parallel:matrix.
const (
	maxNeeds      = 50
	maxMatrixJobs = 200
)

// needsOf lists the jobs j needs: its direct dependencies, then the producers
// of artifacts it downloads.
func needsOf(p *pipeline.Pipeline, artifacts map[string]compiler.Artifact, j *pipeline.Job) []*pipeline.Job {
	downloads := downloadsOf(artifacts, j)
	needs := append([]*pipeline.Job{}, j.Needs...)
	for _, x := range p.OrderedJobs() {
		if downloads[x] && !slices.Contains(needs, x) {
			needs = append(needs, x)
		}
	}
	return needs
}

func downloadsOf(artifacts map[string]compiler.Artifact, j *pipeline.Job) map[*pipeline.Job]bool {
	downloads := map[*pipeline.Job]bool{}
	for _, s := range j.Steps {
		if s.DownloadArtifact != nil {
			downloads[artifacts[s.DownloadArtifact.Name].Producer] = true
		}
	}
	return downloads
}

func (r *renderer) needs(j *pipeline.Job) any {
	downloads := downloadsOf(r.artifacts, j)
	needs := needsOf(r.p, r.artifacts, j)
	plain := true
	for _, n := range needs {
		if producesArtifacts(n) && !downloads[n] {
			plain = false
		}
	}
	if plain {
		var names []string
		for _, n := range needs {
			names = append(names, jobKey(n))
		}
		return shared.FlowSeq(names)
	}
	var entries []*shared.Map
	for _, n := range needs {
		entries = append(entries, shared.NewMap().Set("job", jobKey(n)).Set("artifacts", downloads[n]))
	}
	return entries
}

func (r *renderer) tags(j *pipeline.Job) []string {
	if je, ok := r.cfg.jobs[j]; ok && je.Tags != nil {
		return je.Tags
	}
	if t, ok := r.cfg.ext.RunnerTags[j.OS()]; ok {
		return t
	}
	return defaultTags[j.OS()]
}

func (r *renderer) job(j *pipeline.Job) *shared.Map {
	m := shared.NewMap()
	m.Set("stage", j.Stage.Name)
	if j.Container != nil {
		m.Set("image", value(j.Container.Image))
	}
	m.Set("needs", r.needs(j))
	if cond := condition(shared.InheritedConditions(r.p, j)); cond != "" {
		m.Set("rules", []*shared.Map{shared.NewMap().Set("if", cond)})
	}
	if tags := r.tags(j); len(tags) > 0 {
		m.Set("tags", shared.FlowSeq(tags))
	}
	vars := shared.NewMap()
	if !hasCheckout(j) {
		vars.Set("GIT_STRATEGY", "none")
	}
	for _, e := range j.Env {
		vars.Set(e.Name, value(e.Value))
	}
	if vars.Len() > 0 {
		m.Set("variables", vars)
	}
	if e := j.Environment; e != nil {
		if e.URL != "" {
			m.Set("environment", shared.NewMap().Set("name", e.Name).Set("url", e.URL))
		} else {
			m.Set("environment", e.Name)
		}
	}
	if j.TimeoutMinutes > 0 {
		m.Set("timeout", fmt.Sprintf("%dm", j.TimeoutMinutes))
	}
	if j.Retries > 0 {
		m.Set("retry", j.Retries)
	}
	if len(j.Matrix) > 0 {
		axes := shared.NewMap()
		for _, a := range j.Matrix {
			axes.Set(a.Name, shared.FlowSeq(a.Values))
		}
		m.Set("parallel", shared.NewMap().Set("matrix", []*shared.Map{axes}))
	}
	var caches []*shared.Map
	var artifactPaths, junit []string
	var script []string
	for _, s := range j.Steps {
		switch s.Kind() {
		case pipeline.StepCache:
			key := shared.NewMap()
			if len(s.Cache.Files) > 0 {
				key.Set("prefix", s.Cache.Key).Set("files", shared.FlowSeq(s.Cache.Files))
				caches = append(caches, shared.NewMap().Set("key", key).Set("paths", shared.FlowSeq(s.Cache.Paths)))
			} else {
				caches = append(caches, shared.NewMap().Set("key", s.Cache.Key).Set("paths", shared.FlowSeq(s.Cache.Paths)))
			}
		case pipeline.StepUploadArtifact:
			for _, path := range s.UploadArtifact.Paths {
				if !slices.Contains(artifactPaths, path) {
					artifactPaths = append(artifactPaths, path)
				}
			}
		case pipeline.StepPublishTestResults:
			junit = append(junit, s.PublishTestResults.Path)
		default:
			if shared.IsCommand(s) {
				script = append(script, scriptItems(j, s)...)
			}
		}
	}
	if len(caches) == 1 {
		m.Set("cache", caches[0])
	} else if len(caches) > 1 {
		m.Set("cache", caches)
	}
	if len(script) == 0 {
		script = []string{"echo \"Nothing to run\""}
	}
	seq := shared.Seq()
	for _, line := range script {
		seq.Content = append(seq.Content, shared.Str(line))
	}
	m.Set("script", seq)
	if len(artifactPaths) > 0 || len(junit) > 0 {
		a := shared.NewMap()
		if len(artifactPaths) > 0 {
			a.Set("paths", shared.FlowSeq(artifactPaths))
		}
		if len(junit) > 0 {
			a.Set("reports", shared.NewMap().Set("junit", shared.FlowSeq(junit)))
			a.Set("when", "always")
		}
		m.Set("artifacts", a)
	}
	return m
}

func hasCheckout(j *pipeline.Job) bool {
	for _, s := range j.Steps {
		if s.Kind() == pipeline.StepCheckout {
			return true
		}
	}
	return false
}

// scriptItems renders one step as script entries. Step env and working directories
// are scoped with a subshell so they do not leak into later steps.
func scriptItems(j *pipeline.Job, s *pipeline.Step) []string {
	shell := s.ShellFor(j.OS())
	cmd := shared.Block(shared.Command(s, shell, envName))
	var items []string
	if s.Name != "" {
		items = append(items, "# "+s.Name)
	}
	if len(s.Env) == 0 && s.WorkingDirectory == "" {
		return append(items, cmd)
	}
	var b strings.Builder
	b.WriteString("(\n")
	for _, e := range s.Env {
		fmt.Fprintf(&b, "export %s=%s\n", e.Name, shared.Word(pipeline.MustTemplate(e.Value), shell, envName))
	}
	if s.WorkingDirectory != "" {
		fmt.Fprintf(&b, "cd %s\n", shared.Word(pipeline.MustTemplate(s.WorkingDirectory), shell, envName))
	}
	b.WriteString(strings.TrimRight(cmd, "\n"))
	b.WriteString("\n)\n")
	return append(items, b.String())
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// ruleVar reports whether name is a predefined variable that the generated
// workflow and job rules read.
func ruleVar(name string) bool {
	switch name {
	case "CI_PIPELINE_SOURCE", "CI_COMMIT_BRANCH", "CI_COMMIT_TAG", "CI_DEFAULT_BRANCH":
		return true
	}
	return strings.HasPrefix(name, "CI_MERGE_REQUEST_")
}

// gitVar reports whether name is a predefined variable that ${git.*} reads.
func gitVar(name string) bool {
	return name == "CI_COMMIT_SHA" || name == "CI_COMMIT_REF_NAME"
}
