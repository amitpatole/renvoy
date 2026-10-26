// Package bamboo renders pipelines as Atlassian Bamboo Specs, in YAML (provider
// "bamboo") and Java (provider "bamboo-java"). Both share one plan model and one
// extension namespace, "bamboo".
package bamboo

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/amitpatole/renvoy/internal/compiler"
	"github.com/amitpatole/renvoy/internal/diagnostics"
	"github.com/amitpatole/renvoy/internal/pipeline"
	"github.com/amitpatole/renvoy/internal/providers"
	"github.com/amitpatole/renvoy/internal/providers/shared"
)

// Namespace is the extension namespace both Bamboo providers read.
const Namespace = "bamboo"

// DefaultSpecsVersion is the Bamboo Specs library version used by Java Specs.
// It must match the Bamboo server version.
const DefaultSpecsVersion = "12.1.12"

const branchVariable = "planRepository.branch"

// allBranches is the plan branch pattern for a push trigger without filters.
const allBranches = ".*"

// forbiddenDescription lists characters Bamboo rejects in plan descriptions.
const forbiddenDescription = `"&'<>\`

type extension struct {
	ProjectKey       string              `yaml:"projectKey"`
	PlanKey          string              `yaml:"planKey"`
	Requirements     map[string][]string `yaml:"requirements"`
	LinkedRepository string              `yaml:"linkedRepository"`
	SpecsVersion     string              `yaml:"specsVersion"`
	JavaPackage      string              `yaml:"javaPackage"`
	ServerURL        string              `yaml:"serverUrl"`
}

var (
	keyRe         = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,49}$`)
	requirementRe = regexp.MustCompile(`^[A-Za-z0-9._ -]+(?:=[^"\n\\]+)?$`)
	versionRe     = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	packageRe     = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)*$`)
	repoNameRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]*$`)
	urlRe         = regexp.MustCompile(`^https?://[A-Za-z0-9.-]+(?::[0-9]+)?(?:/[A-Za-z0-9._~/-]*)?$`)
	// reservedRoots are top-level YAML Specs keys that jobs cannot use as names.
	reservedRoots = map[string]bool{"version": true, "plan": true, "docker": true, "repositories": true,
		"triggers": true, "notifications": true, "variables": true, "branches": true, "dependencies": true,
		"branch-overrides": true, "other": true, "stages": true, "labels": true}
	javaKeywords = map[string]bool{"abstract": true, "assert": true, "boolean": true, "break": true, "byte": true,
		"case": true, "catch": true, "char": true, "class": true, "const": true, "continue": true, "default": true,
		"do": true, "double": true, "else": true, "enum": true, "extends": true, "final": true, "finally": true,
		"float": true, "for": true, "goto": true, "if": true, "implements": true, "import": true, "instanceof": true,
		"int": true, "interface": true, "long": true, "native": true, "new": true, "package": true, "private": true,
		"protected": true, "public": true, "return": true, "short": true, "static": true, "strictfp": true,
		"super": true, "switch": true, "synchronized": true, "this": true, "throw": true, "throws": true,
		"transient": true, "try": true, "void": true, "volatile": true, "true": true, "false": true, "null": true}
)

func capabilities(java bool) providers.CapabilitySet {
	checkout := providers.CapabilityInfo{ID: providers.CapCheckout, Support: providers.Emulated,
		Note: "YAML Specs check out the repository first in jobs without a checkout task"}
	if java {
		checkout = providers.CapabilityInfo{ID: providers.CapCheckout, Support: providers.Native, Note: "VcsCheckoutTask"}
	}
	return providers.NewCapabilitySet(
		providers.CapabilityInfo{ID: providers.CapPushTrigger, Support: providers.Native,
			Note: "repository trigger; branches other than the plan branch become plan branches"},
		providers.CapabilityInfo{ID: providers.CapTagTrigger, Support: providers.Unsupported,
			Note: "Bamboo plans do not trigger on tags; use a separate plan or deployment project"},
		providers.CapabilityInfo{ID: providers.CapPullRequestTrigger, Support: providers.Native,
			Note: "plan branches created for pull requests"},
		providers.CapabilityInfo{ID: providers.CapScheduleTrigger, Support: providers.Native,
			Note: "cron trigger converted to Quartz syntax; runs in the Bamboo server time zone"},
		providers.CapabilityInfo{ID: providers.CapStageGraph, Support: providers.Emulated,
			Note: "Bamboo stages run one after another in dependency order"},
		providers.CapabilityInfo{ID: providers.CapJobGraph, Support: providers.Emulated,
			Note: "dependent jobs are placed in later Bamboo stages"},
		providers.CapabilityInfo{ID: providers.CapConditions, Support: providers.Emulated,
			Note: "task conditions on planRepository.branch; exact branch names only"},
		providers.CapabilityInfo{ID: providers.CapStepConditions, Support: providers.Emulated,
			Note: "task conditions on planRepository.branch; exact branch names only"},
		providers.CapabilityInfo{ID: providers.CapRunnerWindows, Support: providers.Native,
			Note: "requires extensions.bamboo.requirements.windows; scripts use Windows PowerShell"},
		providers.CapabilityInfo{ID: providers.CapRunnerMacOS, Support: providers.Native,
			Note: "requires extensions.bamboo.requirements.macos"},
		providers.CapabilityInfo{ID: providers.CapContainer, Support: providers.Native, Note: "job Docker configuration"},
		providers.CapabilityInfo{ID: providers.CapContainerOptions, Support: providers.Native, Note: "docker run arguments"},
		providers.CapabilityInfo{ID: providers.CapMatrix, Support: providers.Unsupported,
			Note: "Bamboo has no job matrix; declare one job per combination"},
		providers.CapabilityInfo{ID: providers.CapTimeout, Support: providers.Unsupported,
			Note: "Bamboo configures hung build detection per plan, not per job"},
		providers.CapabilityInfo{ID: providers.CapRetries, Support: providers.Unsupported,
			Note: "Bamboo cannot retry jobs from Specs; retry inside the step script"},
		providers.CapabilityInfo{ID: providers.CapSecrets, Support: providers.ProviderManaged,
			Note: "create Bamboo variables (encrypted) with the secret names; scripts read them as $bamboo_NAME"},
		providers.CapabilityInfo{ID: providers.CapStepEnv, Support: providers.Native, Note: "script task environment variables"},
		providers.CapabilityInfo{ID: providers.CapWorkingDirectory, Support: providers.Native, Note: "script task working subdirectory"},
		checkout,
		providers.CapabilityInfo{ID: providers.CapReusableSteps, Support: providers.Unsupported,
			Note: "Renvoy does not invent Bamboo plugin task keys; use run steps"},
		providers.CapabilityInfo{ID: providers.CapCache, Support: providers.Unsupported,
			Note: "Bamboo has no cache task; remove the cache step"},
		providers.CapabilityInfo{ID: providers.CapArtifactUpload, Support: providers.Native, Note: "shared artifact; one path per artifact"},
		providers.CapabilityInfo{ID: providers.CapArtifactDownload, Support: providers.Native, Note: "artifact subscription"},
		providers.CapabilityInfo{ID: providers.CapTestResults, Support: providers.Native, Note: "JUnit test parser final task"},
		providers.CapabilityInfo{ID: providers.CapImageBuild, Support: providers.Emulated, Note: "runs the docker CLI"},
		providers.CapabilityInfo{ID: providers.CapImagePush, Support: providers.Emulated, Note: "runs the docker CLI"},
		providers.CapabilityInfo{ID: providers.CapKubernetesDeploy, Support: providers.Emulated, Note: "runs kubectl apply"},
		providers.CapabilityInfo{ID: providers.CapEnvironments, Support: providers.Emulated,
			Note: "plan jobs have no deployment environment; the name is exported as RENVOY_ENVIRONMENT"},
		providers.CapabilityInfo{ID: providers.CapApprovals, Support: providers.Emulated,
			Note: "the job runs in a manual stage that a user must start"},
	)
}

func decode(p *pipeline.Pipeline, provider string) (extension, diagnostics.List) {
	var ext extension
	var diags diagnostics.List
	path := pipeline.JoinKey("extensions", Namespace)
	n := p.Extensions.Get(Namespace)
	if n == nil {
		d := diagnostics.Errorf("", "Bamboo needs a project key and plan key").
			WithHint("add extensions.bamboo.projectKey and extensions.bamboo.planKey, e.g. PROJ and BUILD")
		return ext, diagnostics.List{d.WithProvider(provider)}
	}
	diags = append(diags, pipeline.DecodeStrict(n, &ext, path, nil)...)
	if diags.HasErrors() {
		return ext, withProvider(diags, provider)
	}
	if !keyRe.MatchString(ext.ProjectKey) {
		diags = append(diags, diagnostics.Errorf(path+".projectKey", "projectKey must be an upper-case Bamboo key such as PROJ"))
	}
	if !keyRe.MatchString(ext.PlanKey) {
		diags = append(diags, diagnostics.Errorf(path+".planKey", "planKey must be an upper-case Bamboo key such as BUILD"))
	}
	for _, os := range sortedKeys(ext.Requirements) {
		rp := pipeline.JoinKey(path+".requirements", os)
		if os != pipeline.OSLinux && os != pipeline.OSWindows && os != pipeline.OSMacOS {
			diags = append(diags, diagnostics.Errorf(rp, "unknown runner %q", os).WithHint("use linux, windows, or macos"))
		}
		for i, r := range ext.Requirements[os] {
			if !requirementRe.MatchString(r) {
				diags = append(diags, diagnostics.Errorf(pipeline.JoinIndex(rp, i), "invalid requirement %q", r).
					WithHint(`use a capability key, or key=regex to match its value`))
			}
		}
	}
	if ext.LinkedRepository != "" && !repoNameRe.MatchString(ext.LinkedRepository) {
		diags = append(diags, diagnostics.Errorf(path+".linkedRepository", "invalid repository name %q", ext.LinkedRepository))
	}
	if ext.SpecsVersion != "" && !versionRe.MatchString(ext.SpecsVersion) {
		diags = append(diags, diagnostics.Errorf(path+".specsVersion", "specsVersion must look like %s", DefaultSpecsVersion))
	}
	if ext.JavaPackage != "" {
		ok := packageRe.MatchString(ext.JavaPackage)
		for _, part := range strings.Split(ext.JavaPackage, ".") {
			ok = ok && !javaKeywords[part]
		}
		if !ok {
			diags = append(diags, diagnostics.Errorf(path+".javaPackage", "invalid Java package %q", ext.JavaPackage))
		}
	}
	if ext.ServerURL != "" && !urlRe.MatchString(ext.ServerURL) {
		diags = append(diags, diagnostics.Errorf(path+".serverUrl", "serverUrl must be an http(s) URL without quotes or query strings"))
	}
	for _, j := range p.AllJobs() {
		if j.Extensions.Get(Namespace) != nil {
			diags = append(diags, diagnostics.Warningf(pipeline.JoinKey(j.Path()+".extensions", Namespace),
				"Bamboo has no job-level extensions; this block is ignored"))
		}
	}
	return ext, withProvider(diags, provider)
}

func withProvider(l diagnostics.List, provider string) diagnostics.List {
	for i := range l {
		l[i].Provider = provider
	}
	return l
}

// validate performs the checks shared by both Bamboo providers.
func validate(p *pipeline.Pipeline, prov providers.Provider, java bool) diagnostics.List {
	name := prov.Name()
	diags := providers.CheckCapabilities(p, prov)
	ext, extDiags := decode(p, name)
	diags = append(diags, extDiags...)
	add := func(d diagnostics.Diagnostic) { diags = append(diags, d.WithProvider(name)) }

	if strings.ContainsAny(p.Metadata.Description, forbiddenDescription) {
		add(diagnostics.Errorf("metadata.description", "Bamboo plan descriptions cannot contain any of %s", forbiddenDescription))
	}
	t := p.Triggers
	if t.PullRequest != nil && len(t.PullRequest.Branches) > 0 {
		add(diagnostics.Errorf("triggers.pullRequest.branches", "Bamboo cannot filter pull requests by target branch").
			WithHint("remove the branches filter"))
	}
	if t.Push != nil && t.PullRequest != nil {
		switch branchPattern(t.Push.Branches) {
		case "":
		case allBranches:
			add(diagnostics.Warningf("triggers.pullRequest",
				"Bamboo builds pull requests as plan branches of their source branch, not as merge results"))
		default:
			add(diagnostics.Errorf("triggers.push.branches",
				"Bamboo creates plan branches either for matching branches or for pull requests, not both").
				WithHint("push to all branches, limit push to the plan's branch, or drop the pull request trigger"))
		}
	}
	if t.Push != nil && len(t.Push.Branches) == 1 && branchPattern(t.Push.Branches) == "" {
		add(diagnostics.Infof("triggers.push.branches",
			"Bamboo builds the branch its plan repository uses; make sure that is %q", t.Push.Branches[0]))
	}
	for i, s := range t.Schedules {
		sp := pipeline.JoinIndex("triggers.schedules", i)
		if _, err := Quartz(s.Cron); err != nil {
			add(diagnostics.Errorf(sp+".cron", "%v", err))
		}
		add(diagnostics.Warningf(sp, "Bamboo runs cron triggers in the server time zone on the plan's branch, not in UTC on the listed branches").
			WithHint("adjust the hours for the server time zone and make sure %s is the plan branch", strings.Join(s.Branches, ", ")))
	}
	if java && ext.LinkedRepository == "" && hasCheckoutAnywhere(p) {
		add(diagnostics.Warningf("", "Java Specs plans need a repository for checkout tasks").
			WithHint("set extensions.bamboo.linkedRepository to the linked repository that holds this code"))
	}
	if java && ext.SpecsVersion == "" {
		add(diagnostics.Infof("", "using Bamboo Specs %s; it must match your Bamboo server version", DefaultSpecsVersion).
			WithHint("set extensions.bamboo.specsVersion to your server version"))
	}
	for _, j := range p.AllJobs() {
		jp := j.Path()
		if j.OS() != pipeline.OSLinux && len(ext.Requirements[j.OS()]) == 0 {
			add(diagnostics.Errorf(jp+".runsOn", "Bamboo needs agent requirements to place %s jobs", j.OS()).
				WithHint("set extensions.bamboo.requirements.%s, e.g. [\"operating.system=Windows.*\"]", j.OS()))
		}
		if j.Environment != nil {
			add(diagnostics.Warningf(jp+".environment", "Bamboo plan jobs have no deployment environment; %q is only exported as RENVOY_ENVIRONMENT", j.Environment.Name))
			if j.Environment.RequiresApproval() {
				add(diagnostics.Infof(jp+".environment.approvals", "job %q runs in a manual Bamboo stage that a user must start", j.Name))
			}
		}
		for _, c := range shared.InheritedConditions(p, j) {
			diags = append(diags, checkCondition(c, jp, name)...)
		}
		for _, e := range j.Env {
			diags = append(diags, checkEnvValue(pipeline.JoinKey(jp+".env", e.Name), e.Value, name)...)
		}
		checkout := false
		for _, s := range j.Steps {
			sp := s.Path()
			if s.When != nil {
				diags = append(diags, checkCondition(s.When, sp, name)...)
			}
			for _, e := range s.Env {
				diags = append(diags, checkEnvValue(pipeline.JoinKey(sp+".env", e.Name), e.Value, name)...)
			}
			switch s.Kind() {
			case pipeline.StepCheckout:
				checkout = true
			case pipeline.StepUploadArtifact:
				if len(s.UploadArtifact.Paths) > 1 {
					add(diagnostics.Errorf(sp+".uploadArtifact.paths", "a Bamboo artifact has a single location").
						WithHint("upload a common parent directory or split into separate artifacts"))
				}
			case pipeline.StepRun:
				if shell := s.ShellFor(j.OS()); (shell == pipeline.ShellPwsh) != (j.OS() == pipeline.OSWindows) {
					add(diagnostics.Errorf(sp+".shell", "Bamboo runs %s scripts only on %s agents in Renvoy's mapping", shell,
						map[bool]string{true: "windows", false: "linux and macos"}[shell == pipeline.ShellPwsh]))
				}
			}
		}
		if !java && !checkout {
			add(diagnostics.Infof(jp, "Bamboo YAML Specs check out the repository at the start of job %q because it has no checkout step", j.Name))
		}
		if reservedRoots[j.Name] && !java {
			add(diagnostics.Infof(jp+".name", "%q is a reserved Bamboo YAML key, so the job is named %q", j.Name, yamlJobName(j)))
		}
	}
	layout := buildStages(p)
	if !simpleLayout(p, layout) {
		var names []string
		for _, s := range layout {
			names = append(names, s.Name)
		}
		add(diagnostics.Infof("stages", "Bamboo runs these stages one after another: %s", strings.Join(names, ", ")))
	}
	return diags
}

func hasCheckoutAnywhere(p *pipeline.Pipeline) bool {
	for _, j := range p.AllJobs() {
		for _, s := range j.Steps {
			if s.Kind() == pipeline.StepCheckout {
				return true
			}
		}
	}
	return false
}

func checkCondition(c *pipeline.Condition, path, provider string) diagnostics.List {
	var diags diagnostics.List
	if len(c.Events) > 0 {
		diags = append(diags, diagnostics.Errorf(path+".when.events", "Bamboo task conditions cannot test the triggering event").
			WithHint("remove events, or move the job into a separate plan"))
	}
	if len(c.Branches) > 1 {
		diags = append(diags, diagnostics.Errorf(path+".when.branches", "Bamboo task conditions can match only one branch"))
	}
	for _, b := range c.Branches {
		if _, prefix := pipeline.PatternPrefix(b); prefix {
			diags = append(diags, diagnostics.Errorf(path+".when.branches", "Bamboo task conditions cannot match the branch prefix %q", b))
		}
	}
	return withProvider(diags, provider)
}

// checkEnvValue keeps task environment strings unambiguous. Bamboo substitutes
// variables into one K="v" string and then parses it, so any substituted value
// containing a quote or backslash could add or change environment variables.
// Branch names, plan variables (overridable when running a build), and secrets
// are outside the author's control, so only literals and ${git.sha} are allowed.
func checkEnvValue(path, value, provider string) diagnostics.List {
	if strings.ContainsAny(value, "\"\\\n\r") {
		return diagnostics.List{diagnostics.Errorf(path,
			"Bamboo task environment values cannot contain double quotes, backslashes, or line breaks").WithProvider(provider)}
	}
	t := pipeline.MustTemplate(value)
	if strings.Contains(t.Literal(), "${") {
		return diagnostics.List{diagnostics.Errorf(path,
			"Bamboo task environment values cannot contain ${, which Bamboo substitutes").WithProvider(provider)}
	}
	for _, ref := range t.Refs() {
		if ref.Namespace == pipeline.NSGit && ref.Name == pipeline.GitSHA {
			continue
		}
		return diagnostics.List{diagnostics.Errorf(path,
			"Bamboo cannot pass %s through task environment variables safely; its value could contain quotes", ref).
			WithHint("reference %s in the run script instead", ref).WithProvider(provider)}
	}
	return nil
}

// branchPattern returns a regular expression for push branches that need plan
// branches, or "" when the push trigger names only the plan's own branch.
func branchPattern(branches []string) string {
	if len(branches) == 0 {
		return allBranches
	}
	if len(branches) == 1 {
		if _, prefix := pipeline.PatternPrefix(branches[0]); !prefix {
			return ""
		}
	}
	var alts []string
	for _, b := range branches {
		lit, prefix := pipeline.PatternPrefix(b)
		alt := regexp.QuoteMeta(lit)
		if prefix {
			alt += ".*"
		}
		alts = append(alts, alt)
	}
	return "^(" + strings.Join(alts, "|") + ")$"
}

// Quartz converts a five-field cron expression to Bamboo's Quartz syntax.
func Quartz(expr string) (string, error) {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return "", fmt.Errorf("cron expression %q must have five fields", expr)
	}
	dom, dow := f[2], f[4]
	switch {
	case dow == "*":
		dow = "?"
	case dom == "*":
		dom = "?"
		var err error
		if dow, err = quartzDays(dow); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("cannot schedule on both a day of month and a day of week in Bamboo (%q)", expr)
	}
	return strings.Join([]string{"0", f[0], f[1], dom, f[3], dow}, " "), nil
}

// quartzDays shifts day-of-week numbers from cron (0=Sunday) to Quartz (1=Sunday).
func quartzDays(field string) (string, error) {
	items := strings.Split(field, ",")
	for i, item := range items {
		base, step, hasStep := strings.Cut(item, "/")
		if base != "*" {
			bounds := strings.Split(base, "-")
			for k, b := range bounds {
				n, err := strconv.Atoi(b)
				if err != nil {
					return "", fmt.Errorf("invalid day of week %q", field)
				}
				bounds[k] = strconv.Itoa(n + 1)
			}
			base = strings.Join(bounds, "-")
		}
		if hasStep {
			base += "/" + step
		}
		items[i] = base
	}
	return strings.Join(items, ","), nil
}

// Plan model shared by both renderers.

type plan struct {
	ProjectKey, PlanKey, Name, Description string
	Variables                              pipeline.Vars
	PollRepository                         bool
	Crons                                  []string
	BranchCreate                           string // "", "for-new-branch", "for-pull-request"
	BranchRegex                            string
	Stages                                 []*stage
}

type stage struct {
	Name   string
	Manual bool
	Jobs   []*job
}

type job struct {
	Name, Key     string
	Image         string
	RunArgs       string
	Requirements  []requirement
	Tasks         []task
	FinalTasks    []task
	Artifacts     []artifact
	Subscriptions []artifact
}

type requirement struct{ Key, Match string }

type artifact struct{ Name, Location, Pattern string }

type taskKind int

const (
	taskCheckout taskKind = iota
	taskScript
	taskJUnit
)

type task struct {
	Kind        taskKind
	Description string
	PowerShell  bool
	Body        string
	Env         string
	WorkDir     string
	Branches    []string // AND of planRepository.branch equals conditions
	Results     string
}

// buildPlan converts a validated, compiled pipeline into the Bamboo plan model.
func buildPlan(p *pipeline.Pipeline, ext extension) *plan {
	pl := &plan{ProjectKey: ext.ProjectKey, PlanKey: ext.PlanKey, Name: p.Metadata.Name,
		Description: p.Metadata.Description, Variables: p.Variables}
	t := p.Triggers
	pl.PollRepository = t.Push != nil || t.PullRequest != nil
	for _, s := range t.Schedules {
		q, _ := Quartz(s.Cron)
		pl.Crons = append(pl.Crons, q)
	}
	switch {
	case t.Push != nil && branchPattern(t.Push.Branches) == allBranches:
		pl.BranchCreate = "for-new-branch"
		pl.BranchRegex = allBranches
	case t.PullRequest != nil:
		pl.BranchCreate = "for-pull-request"
	case t.Push != nil && branchPattern(t.Push.Branches) != "":
		pl.BranchCreate = "for-new-branch"
		pl.BranchRegex = branchPattern(t.Push.Branches)
	}
	artifacts := compiler.Artifacts(p)
	keys := jobKeys(p)
	for _, ls := range buildStages(p) {
		st := &stage{Name: ls.Name, Manual: ls.Manual}
		for _, j := range ls.Jobs {
			st.Jobs = append(st.Jobs, buildJob(p, j, keys[j], ext, artifacts))
		}
		pl.Stages = append(pl.Stages, st)
	}
	return pl
}

type layoutStage struct {
	Name   string
	Manual bool
	Jobs   []*pipeline.Job
}

// buildStages splits each Renvoy stage into Bamboo stages: jobs are grouped by
// dependency depth, and jobs that need approval run in their own manual stage.
func buildStages(p *pipeline.Pipeline) []layoutStage {
	var out []layoutStage
	for _, s := range p.Order {
		depth := map[*pipeline.Job]int{}
		maxDepth := 0
		for _, j := range s.JobOrder {
			for _, d := range j.SameStageNeeds {
				depth[j] = max(depth[j], depth[d]+1)
			}
			maxDepth = max(maxDepth, depth[j])
		}
		count := 0
		next := func(manual bool, jobs []*pipeline.Job) {
			if len(jobs) == 0 {
				return
			}
			count++
			name := s.Name
			if count > 1 {
				name = fmt.Sprintf("%s (%d)", s.Name, count)
			}
			out = append(out, layoutStage{Name: name, Manual: manual, Jobs: jobs})
		}
		for d := 0; d <= maxDepth; d++ {
			var normal, gated []*pipeline.Job
			for _, j := range s.JobOrder {
				if depth[j] != d {
					continue
				}
				if j.Environment.RequiresApproval() {
					gated = append(gated, j)
				} else {
					normal = append(normal, j)
				}
			}
			next(false, normal)
			next(true, gated)
		}
	}
	return out
}

// simpleLayout reports whether the Bamboo stages add no ordering beyond the pipeline's.
func simpleLayout(p *pipeline.Pipeline, layout []layoutStage) bool {
	if len(layout) != len(p.Stages) {
		return false
	}
	for i, s := range p.Order {
		if i == 0 && len(s.Needs) != 0 || i > 0 && (len(s.Needs) != 1 || s.Needs[0] != p.Order[i-1]) {
			return false
		}
	}
	return true
}

// jobKeys derives unique upper-case Bamboo job keys from job names.
func jobKeys(p *pipeline.Pipeline) map[*pipeline.Job]string {
	keys := map[*pipeline.Job]string{}
	used := map[string]bool{}
	for _, j := range p.AllJobs() {
		base := strings.ToUpper(strings.ReplaceAll(j.Name, "-", ""))
		if len(base) > 40 {
			base = base[:40]
		}
		if len(base) < 2 {
			base += "JOB"
		}
		key := base
		for n := 2; used[key]; n++ {
			key = base + strconv.Itoa(n)
		}
		used[key] = true
		keys[j] = key
	}
	return keys
}

func yamlJobName(j *pipeline.Job) string {
	if reservedRoots[j.Name] {
		return j.Stage.Name + " " + j.Name
	}
	return j.Name
}

// envName maps references to the environment variables Bamboo exports to scripts.
func envName(ref pipeline.Ref) string {
	switch ref.Namespace {
	case pipeline.NSGit:
		if ref.Name == pipeline.GitBranch {
			return "bamboo_planRepository_branchName"
		}
		return "bamboo_planRepository_revision"
	default:
		return "bamboo_" + ref.Name
	}
}

// fieldValue renders a template for a Bamboo configuration field, where
// ${bamboo.name} variables are substituted.
func fieldValue(value string) string {
	var b strings.Builder
	for _, seg := range pipeline.MustTemplate(value) {
		switch {
		case seg.Ref == nil:
			b.WriteString(seg.Literal)
		case seg.Ref.Namespace == pipeline.NSGit && seg.Ref.Name == pipeline.GitBranch:
			b.WriteString("${bamboo.planRepository.branchName}")
		case seg.Ref.Namespace == pipeline.NSGit:
			b.WriteString("${bamboo.planRepository.revision}")
		default:
			b.WriteString("${bamboo." + seg.Ref.Name + "}")
		}
	}
	return b.String()
}

func buildJob(p *pipeline.Pipeline, j *pipeline.Job, key string, ext extension, artifacts map[string]compiler.Artifact) *job {
	bj := &job{Name: j.Name, Key: key}
	if c := j.Container; c != nil {
		for _, seg := range pipeline.MustTemplate(c.Image) {
			if seg.Ref != nil {
				v, _ := p.Variables.Get(seg.Ref.Name)
				bj.Image += v
			} else {
				bj.Image += seg.Literal
			}
		}
		bj.RunArgs = c.Options
	}
	for _, r := range ext.Requirements[j.OS()] {
		k, m, _ := strings.Cut(r, "=")
		bj.Requirements = append(bj.Requirements, requirement{Key: k, Match: m})
	}
	var jobBranches []string
	for _, c := range shared.InheritedConditions(p, j) {
		jobBranches = append(jobBranches, c.Branches...)
	}
	var env []string
	if j.Environment != nil {
		env = append(env, `RENVOY_ENVIRONMENT="`+j.Environment.Name+`"`)
	}
	for _, e := range j.Env {
		env = append(env, e.Name+`="`+fieldValue(e.Value)+`"`)
	}
	for _, s := range j.Steps {
		branches := jobBranches
		if s.When != nil {
			branches = append(append([]string{}, jobBranches...), s.When.Branches...)
		}
		switch s.Kind() {
		case pipeline.StepCheckout:
			bj.Tasks = append(bj.Tasks, task{Kind: taskCheckout, Branches: branches})
		case pipeline.StepUploadArtifact:
			bj.Artifacts = append(bj.Artifacts, artifactFor(s.UploadArtifact.Name, s.UploadArtifact.Paths[0]))
		case pipeline.StepDownloadArtifact:
			a := artifacts[s.DownloadArtifact.Name]
			up := artifactFor(a.Name, a.Step.UploadArtifact.Paths[0])
			bj.Subscriptions = append(bj.Subscriptions, artifact{Name: up.Name, Location: up.Location})
		case pipeline.StepPublishTestResults:
			bj.FinalTasks = append(bj.FinalTasks, task{Kind: taskJUnit, Results: s.PublishTestResults.Path, Branches: branches})
		default:
			shell := s.ShellFor(j.OS())
			body := shared.Command(s, shell, envName)
			switch shell {
			case pipeline.ShellBash:
				body = "#!/bin/bash\nset -eo pipefail\n" + body
			case pipeline.ShellSh:
				body = "#!/bin/sh\nset -e\n" + body
			}
			stepEnv := append([]string{}, env...)
			for _, e := range s.Env {
				stepEnv = append(stepEnv, e.Name+`="`+fieldValue(e.Value)+`"`)
			}
			bj.Tasks = append(bj.Tasks, task{Kind: taskScript, Description: s.Name, PowerShell: shell == pipeline.ShellPwsh,
				Body: strings.TrimRight(body, "\n"), Env: strings.Join(stepEnv, " "), WorkDir: s.WorkingDirectory, Branches: branches})
		}
	}
	return bj
}

// artifactFor maps a workspace path to a Bamboo artifact location and pattern.
func artifactFor(name, path string) artifact {
	if strings.HasSuffix(path, "/") {
		return artifact{Name: name, Location: strings.TrimSuffix(path, "/"), Pattern: "**/*"}
	}
	dir, file := ".", path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		dir, file = path[:i], path[i+1:]
	}
	return artifact{Name: name, Location: dir, Pattern: file}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
