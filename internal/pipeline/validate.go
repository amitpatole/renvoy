package pipeline

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/amitpatole/renvoy/internal/diagnostics"
)

var (
	// NameRe constrains pipeline, stage, and job names. Kebab-case maps cleanly to every
	// provider's identifier rules and to safe file names.
	NameRe         = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	envNameRe      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	environmentRe  = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,62})$`)
	artifactNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	cacheKeyRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	namespaceRe    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	branchPatRe    = regexp.MustCompile(`^[A-Za-z0-9._/-]+\*?$`)
	cronFieldRe    = regexp.MustCompile(`^[0-9*/,-]+$`)
	usesRefRe      = regexp.MustCompile(`^[^\s'"\x60$\\]+$`)
)

// MaxSchedules bounds triggers.schedules.
const MaxSchedules = 20

// MaxLabels bounds metadata.labels.
const MaxLabels = 100

// MaxJobs bounds the number of jobs, which bounds the dependency graph that every
// provider walks. GitHub Actions itself allows 256 jobs per workflow.
const MaxJobs = 500

// MaxVariableValue bounds a variable value, since some providers inline values.
const MaxVariableValue = 4096

// MaxInlinedBytes bounds the total size of variable values substituted at
// references, which bounds how much rendering can amplify the input.
const MaxInlinedBytes = 1 << 20

// MaxTotalMatrixCombinations bounds matrix expansion across all jobs.
const MaxTotalMatrixCombinations = 1024

// MaxMatrixAxes bounds the axes of one matrix.
const MaxMatrixAxes = 16

// MaxMatrixBytes bounds matrix text multiplied by its combinations, since Azure
// writes every axis into every combination.
const MaxMatrixBytes = 1 << 20

// MaxMatrixCombinations bounds matrix expansion.
const MaxMatrixCombinations = 256

// reservedEnvPrefix is used for bindings Renvoy generates.
const reservedEnvPrefix = "RENVOY_"

// Validate performs field-level semantic validation. knownExtensions lists the
// provider extension namespaces compiled into this binary.
func Validate(p *Pipeline, knownExtensions []string) diagnostics.List {
	v := &validator{p: p, known: knownExtensions, axisOwner: map[string]bool{}}
	v.run()
	return v.diags
}

type validator struct {
	p         *Pipeline
	known     []string
	diags     diagnostics.List
	axisOwner map[string]bool
}

func (v *validator) add(d diagnostics.Diagnostic) { v.diags = append(v.diags, d) }

func (v *validator) errorf(path, format string, args ...any) *diagnostics.Diagnostic {
	v.diags = append(v.diags, diagnostics.Errorf(path, format, args...))
	return &v.diags[len(v.diags)-1]
}

func (v *validator) run() {
	p := v.p
	switch p.APIVersion {
	case APIVersionV1Alpha1:
	case "":
		v.errorf("apiVersion", "apiVersion is required").Hint = "set apiVersion: " + APIVersionV1Alpha1
	default:
		v.errorf("apiVersion", "unsupported apiVersion %q", p.APIVersion).Hint = "this Renvoy supports " + APIVersionV1Alpha1
	}
	switch p.Kind {
	case KindPipeline:
	case "":
		v.errorf("kind", "kind is required").Hint = "set kind: Pipeline"
	default:
		v.errorf("kind", "unsupported kind %q", p.Kind).Hint = "the only supported kind is Pipeline"
	}
	if v.checkName("metadata.name", p.Metadata.Name, "pipeline") && windowsDeviceRe.MatchString(p.Metadata.Name) {
		v.errorf("metadata.name", "pipeline name %q is a reserved Windows device name, so generated files named after it cannot be checked out on Windows", p.Metadata.Name)
	}
	if len(p.Metadata.Labels) > MaxLabels {
		v.errorf("metadata.labels", "%d labels are defined; at most %d are allowed", len(p.Metadata.Labels), MaxLabels)
	}
	v.checkNoMarkers(reflect.ValueOf(p).Elem(), "")
	v.checkTriggers()

	folded := map[string]string{}
	for _, va := range p.Variables {
		path := JoinKey("variables", va.Name)
		v.checkEnvName(path, va.Name, "variable")
		if prev, dup := folded[strings.ToUpper(va.Name)]; dup {
			v.errorf(path, "variable %q differs from %q only by case", va.Name, prev).
				Hint = "Azure DevOps exposes variables as upper-case environment variables, so these would collide"
		}
		folded[strings.ToUpper(va.Name)] = va.Name
		if t, err := ParseTemplate(va.Value); err != nil {
			v.errorf(path, "%v", err)
		} else if !t.IsLiteral() {
			v.errorf(path, "variable values must be literal; found reference %s", t.Refs()[0]).
				Hint = "reference other values from steps instead"
		}
		v.warnProviderSyntax(path, va.Value)
		v.checkControlEnv(path, va.Name, va.Value)
		if len(va.Value) > MaxVariableValue {
			v.errorf(path, "variable value is %d bytes; at most %d are allowed", len(va.Value), MaxVariableValue)
		}
	}
	seenSecrets := map[string]bool{}
	for i, s := range p.Secrets {
		path := JoinIndex("secrets", i)
		v.checkEnvName(path, s, "secret")
		if seenSecrets[s] {
			v.errorf(path, "secret %q is declared more than once", s)
		}
		seenSecrets[s] = true
		if prev, ok := folded[strings.ToUpper(s)]; ok {
			v.errorf(path, "secret %q has the same name as variable %q (ignoring case)", s, prev)
		}
	}
	v.checkExtensions("extensions", p.Extensions)

	if len(p.Stages) == 0 {
		v.errorf("stages", "at least one stage is required")
	}
	stageNames := map[string]bool{}
	for i, s := range p.Stages {
		path := JoinIndex("stages", i)
		if v.checkName(path+".name", s.Name, "stage") {
			if stageNames[s.Name] {
				v.errorf(path+".name", "duplicate stage name %q", s.Name)
			}
			stageNames[s.Name] = true
		}
		v.checkCondition(path+".when", s.When)
		if len(s.Jobs) == 0 {
			v.errorf(path+".jobs", "stage %q has no jobs", s.Name)
		}
	}
	if n := len(p.AllJobs()); n > MaxJobs {
		v.errorf("stages", "the pipeline has %d jobs; at most %d are supported", n, MaxJobs)
		return
	}
	jobNames := map[string]bool{}
	for i, s := range p.Stages {
		for j, job := range s.Jobs {
			path := fmt.Sprintf("stages[%d].jobs[%d]", i, j)
			if v.checkName(path+".name", job.Name, "job") {
				switch {
				case jobNames[job.Name]:
					v.errorf(path+".name", "duplicate job name %q; job names must be unique across the pipeline", job.Name)
				case stageNames[job.Name]:
					v.errorf(path+".name", "job name %q is also a stage name", job.Name).
						Hint = "Jenkins requires unique stage names, and Renvoy renders jobs as Jenkins stages"
				}
				jobNames[job.Name] = true
			}
			v.checkJob(path, job)
		}
	}
	v.checkBudgets()
}

// checkBudgets bounds how much a pipeline can grow when providers render it.
func (v *validator) checkBudgets() {
	combos, inlined, matrixBytes := 0, 0, 0
	count := func(texts ...string) {
		for _, s := range texts {
			t, err := ParseTemplate(s)
			if err != nil {
				continue
			}
			for _, r := range t.Refs() {
				if r.Namespace == NSVariables {
					val, _ := v.p.Variables.Get(r.Name)
					inlined += len(val)
				}
			}
		}
	}
	vals := func(vs Vars) []string {
		out := make([]string, len(vs))
		for i, e := range vs {
			out[i] = e.Value
		}
		return out
	}
	for _, j := range v.p.AllJobs() {
		// n saturates just above the limit, so huge matrices cannot overflow or
		// drive the work below; checkMatrix reports the limit itself.
		n, axisBytes := 1, 0
		for _, a := range j.Matrix {
			n = min(n*max(len(a.Values), 1), MaxMatrixCombinations+1)
			longest := 0
			for _, val := range a.Values {
				longest = max(longest, len(val))
			}
			axisBytes += len(a.Name) + longest + 8
		}
		combos += n
		if len(j.Matrix) > 0 {
			matrixBytes += n * axisBytes
		}
		count(vals(j.Env)...)
		if j.Container != nil {
			// Azure resolves the image once per matrix combination.
			before := inlined
			count(j.Container.Image)
			inlined += (inlined - before) * (n - 1)
		}
		for _, s := range j.Steps {
			count(s.Run)
			count(vals(s.Env)...)
			count(vals(s.With)...)
			if s.BuildImage != nil {
				count(s.BuildImage.Image)
				count(vals(s.BuildImage.BuildArgs)...)
			}
			if s.PushImage != nil {
				count(s.PushImage.Image)
			}
		}
	}
	if combos > MaxTotalMatrixCombinations+len(v.p.AllJobs()) {
		v.errorf("stages", "matrix jobs expand to %d combinations in total; at most %d are supported", combos, MaxTotalMatrixCombinations)
	}
	if matrixBytes > MaxMatrixBytes {
		v.errorf("stages", "matrix jobs would render about %d bytes of matrix entries; at most %d are supported", matrixBytes, MaxMatrixBytes).
			Hint = "use fewer axes or shorter values"
	}
	if inlined > MaxInlinedBytes {
		v.errorf("variables", "variable references expand to %d bytes; at most %d are supported", inlined, MaxInlinedBytes).
			Hint = "reference large values from a file in the repository instead"
	}
}

var windowsDeviceRe = regexp.MustCompile(`^(con|prn|aux|nul|com[0-9]|lpt[0-9])$`)

func (v *validator) checkName(path, name, what string) bool {
	if name == "" {
		v.errorf(path, "%s name is required", what)
		return false
	}
	if !NameRe.MatchString(name) {
		v.errorf(path, "invalid %s name %q", what, name).
			Hint = "use lowercase letters, digits, and hyphens, starting with a letter (at most 63 characters)"
		return false
	}
	return true
}

func (v *validator) checkEnvName(path, name, what string) {
	if len(name) > 128 {
		v.errorf(path, "%s name is %d characters; at most 128 are allowed", what, len(name))
		return
	}
	if !envNameRe.MatchString(name) {
		v.errorf(path, "invalid %s name %q", what, name).
			Hint = "use letters, digits, and underscores, starting with a letter or underscore"
		return
	}
	if strings.HasPrefix(strings.ToUpper(name), reservedEnvPrefix) {
		v.errorf(path, "%s name %q uses the reserved prefix %s", what, name, reservedEnvPrefix)
	}
}

func (v *validator) checkTriggers() {
	t := v.p.Triggers
	if t.Push == nil && t.PullRequest == nil && t.Manual == nil && len(t.Schedules) == 0 {
		v.errorf("triggers", "at least one trigger is required").
			Hint = "add triggers.push, triggers.pullRequest, triggers.manual, or triggers.schedules"
	}
	if t.Push != nil {
		v.checkPatterns("triggers.push.branches", t.Push.Branches)
		v.checkPatterns("triggers.push.tags", t.Push.Tags)
	}
	if t.PullRequest != nil {
		v.checkPatterns("triggers.pullRequest.branches", t.PullRequest.Branches)
	}
	if len(t.Schedules) > MaxSchedules {
		v.errorf("triggers.schedules", "%d schedules are listed; at most %d are allowed", len(t.Schedules), MaxSchedules)
	}
	for i, s := range t.Schedules {
		path := JoinIndex("triggers.schedules", i)
		if len(s.Branches) > MaxPatterns {
			v.errorf(path+".branches", "%d branches are listed; at most %d are allowed", len(s.Branches), MaxPatterns)
		}
		if err := ValidateCron(s.Cron); err != nil {
			v.errorf(path+".cron", "%v", err).Hint = `use five fields: minute hour day-of-month month day-of-week, e.g. "0 3 * * 1"`
		}
		if len(s.Branches) == 0 {
			v.errorf(path+".branches", "a schedule must list the branches it runs on")
		}
		for j, b := range s.Branches {
			if _, prefix := PatternPrefix(b); prefix || ValidatePattern(b) != nil {
				v.errorf(JoinIndex(path+".branches", j), "schedule branches must be exact branch names; found %q", b)
			}
		}
	}
}

// MaxPatternLength bounds a branch or tag pattern (git limits ref components similarly).
const MaxPatternLength = 255

// MaxPatterns bounds the patterns in one list.
const MaxPatterns = 100

func (v *validator) checkPatterns(path string, pats []string) {
	if len(pats) > MaxPatterns {
		v.errorf(path, "%d patterns are listed; at most %d are allowed", len(pats), MaxPatterns)
	}
	for i, pat := range pats {
		if err := ValidatePattern(pat); err != nil {
			v.errorf(JoinIndex(path, i), "%v", err).
				Hint = `use an exact name such as "main" or a prefix ending in "*" such as "release/*"`
		}
	}
}

// ValidatePattern checks a branch or tag pattern: an exact name, optionally ending
// in "*" to match every name with that prefix.
func ValidatePattern(pat string) error {
	if len(pat) > MaxPatternLength {
		return fmt.Errorf("pattern is %d characters; at most %d are allowed", len(pat), MaxPatternLength)
	}
	if pat == "" || pat == "*" {
		return fmt.Errorf("invalid pattern %q", pat)
	}
	if !branchPatRe.MatchString(pat) || strings.Contains(pat, "..") || strings.HasPrefix(pat, "/") ||
		strings.HasPrefix(pat, "-") || strings.Contains(pat, "//") {
		return fmt.Errorf("unsupported pattern %q", pat)
	}
	return nil
}

// PatternPrefix splits a pattern into its literal part and whether it is a prefix match.
func PatternPrefix(pat string) (string, bool) {
	if strings.HasSuffix(pat, "*") {
		return strings.TrimSuffix(pat, "*"), true
	}
	return pat, false
}

// ValidateCron checks a five-field cron expression with numeric fields.
func ValidateCron(expr string) error {
	if len(expr) > 256 {
		return fmt.Errorf("cron expression is %d characters; at most 256 are allowed", len(expr))
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return fmt.Errorf("cron expression %q must have exactly five fields", expr)
	}
	limits := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
	names := [5]string{"minute", "hour", "day-of-month", "month", "day-of-week"}
	for i, f := range fields {
		if !cronFieldRe.MatchString(f) {
			return fmt.Errorf("cron %s field %q may contain only digits and * / , -", names[i], f)
		}
		for _, item := range strings.Split(f, ",") {
			if err := cronItem(item, limits[i][0], limits[i][1]); err != nil {
				return fmt.Errorf("cron %s field %q: %w", names[i], f, err)
			}
		}
	}
	return nil
}

var cronItemRe = regexp.MustCompile(`^(\*|[0-9]{1,2}(?:-[0-9]{1,2})?)(?:/([0-9]{1,2}))?$`)

// cronItem validates one comma-separated cron item: *, n, a-b, optionally /step.
func cronItem(item string, lo, hi int) error {
	m := cronItemRe.FindStringSubmatch(item)
	if m == nil {
		return fmt.Errorf("%q is not *, a number, or a range, optionally with /step", item)
	}
	if m[2] != "" {
		if step, _ := strconv.Atoi(m[2]); step < 1 || step > hi-lo+1 {
			return fmt.Errorf("step %d must be between 1 and %d", step, hi-lo+1)
		}
	}
	if m[1] == "*" {
		return nil
	}
	bounds := strings.Split(m[1], "-")
	a, _ := strconv.Atoi(bounds[0])
	b := a
	if len(bounds) == 2 {
		b, _ = strconv.Atoi(bounds[1])
	}
	if a < lo || b > hi || a > b {
		return fmt.Errorf("%q is outside %d-%d or reversed", item, lo, hi)
	}
	return nil
}

func (v *validator) checkCondition(path string, c *Condition) {
	if c == nil {
		return
	}
	if c.IsEmpty() {
		v.errorf(path, "condition is empty").Hint = "set branches or events, or remove when"
	}
	v.checkPatterns(path+".branches", c.Branches)
	for i, e := range c.Events {
		if !slices.Contains(Events, e) {
			v.errorf(JoinIndex(path+".events", i), "unknown event %q", e).
				Hint = "use one of " + strings.Join(Events, ", ")
		}
	}
}

func (v *validator) checkExtensions(path string, ext Extensions) {
	for _, x := range ext {
		if !slices.Contains(v.known, x.Namespace) {
			v.add(diagnostics.Warningf(JoinKey(path, x.Namespace),
				"unknown extension namespace %q is ignored by every provider", x.Namespace).
				WithHint("known namespaces: %s", strings.Join(v.known, ", ")))
		}
	}
}

func (v *validator) checkJob(path string, j *Job) {
	switch j.RunsOn {
	case "", OSLinux, OSWindows, OSMacOS:
	default:
		v.errorf(path+".runsOn", "unknown runner %q", j.RunsOn).
			Hint = "use linux, windows, or macos; map custom runner labels in provider extensions"
	}
	if j.TimeoutMinutes < 0 {
		v.errorf(path+".timeoutMinutes", "timeoutMinutes cannot be negative")
	}
	if j.Retries < 0 || j.Retries > 10 {
		v.errorf(path+".retries", "retries must be between 0 and 10")
	}
	v.checkCondition(path+".when", j.When)
	v.checkExtensions(path+".extensions", j.Extensions)

	axes := map[string]bool{}
	if j.Matrix != nil {
		v.checkMatrix(path+".matrix", j.Matrix, axes)
	}
	if j.Container != nil {
		cp := path + ".container"
		if j.Container.Image == "" {
			v.errorf(cp+".image", "container image is required")
		} else {
			v.checkTemplate(cp+".image", j.Container.Image, axes, NSVariables, NSMatrix)
			v.checkExpandedImage(cp+".image", j.Container.Image)
		}
		if j.OS() == OSWindows || j.OS() == OSMacOS {
			v.errorf(cp, "job containers require a linux runner")
		}
		if strings.ContainsAny(j.Container.Options, "\n\r") {
			v.errorf(cp+".options", "container options must be a single line")
		}
		v.warnProviderSyntax(cp+".image", j.Container.Image)
		v.warnProviderSyntax(cp+".options", j.Container.Options)
	}
	if e := j.Environment; e != nil {
		ep := path + ".environment"
		switch {
		case e.Name == "":
			v.errorf(ep+".name", "environment name is required")
		case !environmentRe.MatchString(e.Name):
			v.errorf(ep+".name", "invalid environment name %q", e.Name).
				Hint = "use letters, digits, '.', '_' and '-' (at most 63 characters)"
		}
		if e.URL != "" && !strings.HasPrefix(e.URL, "https://") && !strings.HasPrefix(e.URL, "http://") {
			v.errorf(ep+".url", "environment url must start with https:// or http://")
		}
		if strings.ContainsAny(e.URL, " \t\n'\"`$") {
			v.errorf(ep+".url", "environment url must be a literal URL without whitespace, quotes, or references")
		}
	}
	v.checkEnv(path+".env", j.Env, axes)

	if len(j.Steps) == 0 {
		v.errorf(path+".steps", "job %q has no steps", j.Name)
	}
	checkouts := 0
	for i, s := range j.Steps {
		sp := JoinIndex(path+".steps", i)
		if s.Kind() == StepCheckout {
			checkouts++
			if checkouts == 2 {
				v.add(diagnostics.Warningf(sp, "job %q checks out the repository more than once", j.Name))
			}
		}
		v.checkStep(sp, s, j, axes)
	}
}

func (v *validator) checkMatrix(path string, m Matrix, axes map[string]bool) {
	if len(m) == 0 {
		v.errorf(path, "matrix has no axes")
		return
	}
	if len(m) > MaxMatrixAxes {
		v.errorf(path, "matrix has %d axes; at most %d are allowed", len(m), MaxMatrixAxes)
		return
	}
	combos := 1
	folded := map[string]bool{}
	for _, axis := range m {
		ap := JoinKey(path, axis.Name)
		if folded[strings.ToUpper(axis.Name)] {
			v.errorf(ap, "matrix axis %q differs from another axis only by case", axis.Name)
		}
		folded[strings.ToUpper(axis.Name)] = true
		v.checkEnvName(ap, axis.Name, "matrix axis")
		if isControlEnvName(axis.Name) {
			v.errorf(ap, "matrix axis %q would set an environment variable that changes how shells or tools behave", axis.Name)
		}
		if v.takenName(axis.Name) {
			v.errorf(ap, "matrix axis %q has the same name as a variable or secret (ignoring case)", axis.Name)
		}
		axes[axis.Name] = true
		if len(axis.Values) == 0 {
			v.errorf(ap, "matrix axis %q has no values", axis.Name)
		}
		seen := map[string]bool{}
		for i, val := range axis.Values {
			if seen[val] {
				v.errorf(JoinIndex(ap, i), "duplicate matrix value %q", val)
			}
			seen[val] = true
			if !matrixValueRe.MatchString(val) {
				v.errorf(JoinIndex(ap, i), "matrix value %q may contain only letters, digits, '.', '_', '-' and '/'", val)
			}
		}
		combos *= max(len(axis.Values), 1)
		if combos > MaxMatrixCombinations {
			v.errorf(path, "matrix expands to more than %d combinations", MaxMatrixCombinations)
			return
		}
	}
}

var matrixValueRe = regexp.MustCompile(`^[A-Za-z0-9._/][A-Za-z0-9._/-]{0,63}$`)

func (v *validator) checkEnv(path string, env Vars, axes map[string]bool) {
	folded := map[string]bool{}
	for _, e := range env {
		ep := JoinKey(path, e.Name)
		// Windows and Azure Pipelines treat environment names case-insensitively.
		if folded[strings.ToUpper(e.Name)] {
			v.errorf(ep, "environment variable %q differs from another entry only by case", e.Name)
		}
		folded[strings.ToUpper(e.Name)] = true
		v.checkEnvName(ep, e.Name, "environment variable")
		if v.takenName(e.Name) || hasFold(axes, e.Name) {
			d := v.errorf(ep, "environment variable %q shadows a variable, secret, or matrix axis of the same name", e.Name)
			d.Hint = "rename it; shadowing would change what ${...} references resolve to on some providers"
			if slices.ContainsFunc(v.p.Secrets, func(s string) bool { return strings.EqualFold(s, e.Name) }) {
				d.Hint = "run steps already receive the secret as ${secrets." + e.Name + "}; to pass it to a tool under this name, reference it in the script"
			}
		}
		v.checkTemplate(ep, e.Value, axes, NSVariables, NSSecrets, NSGit, NSMatrix)
		v.warnProviderSyntax(ep, e.Value)
		v.checkControlEnv(ep, e.Name, e.Value)
	}
}

// warnProviderSyntax flags provider expression syntax in values, which some
// providers evaluate wherever it appears in the generated file.
func (v *validator) warnProviderSyntax(path, value string) {
	lit := literalText(value)
	if strings.Contains(lit, "${{") || strings.Contains(lit, "$[") || strings.Contains(lit, "$(") || providerVarRe.MatchString(lit) {
		v.add(diagnostics.Warningf(path, "value contains provider expression syntax (${{, $[, $( or ${bamboo.}), which some providers evaluate").
			WithHint("use portable references such as ${secrets.NAME} or ${git.sha}"))
	}
}

// providerVarRe matches Bamboo-style ${bamboo.x} / ${system.x} substitutions.
var providerVarRe = regexp.MustCompile(`\$\{(?:bamboo|system)\.`)

// azureMacroRe matches dotted Azure macros such as $(Build.SourceBranchName),
// which Azure substitutes into script text before the shell runs it. Plain $(cmd)
// is shell command substitution and is not flagged.
var azureMacroRe = regexp.MustCompile(`\$\([A-Za-z_][A-Za-z0-9_]*\.[A-Za-z0-9_.]+\)`)

// literalText returns s with $${ escapes resolved and references removed.
func literalText(s string) string {
	t, err := ParseTemplate(s)
	if err != nil {
		return s
	}
	var b strings.Builder
	for _, seg := range t {
		b.WriteString(seg.Literal)
	}
	return b.String()
}

// controlEnvNames change how shells, runtimes, dynamic linkers, or git behave.
// Setting them runs code that appears in no run step, so author-set values warn.
var controlEnvNames = map[string]bool{
	"BASH_ENV": true, "ENV": true, "SHELLOPTS": true, "BASHOPTS": true, "PS4": true, "PROMPT_COMMAND": true,
	"IFS": true, "PATH": true, "LD_PRELOAD": true, "LD_LIBRARY_PATH": true, "LD_AUDIT": true,
	"DYLD_INSERT_LIBRARIES": true, "DYLD_LIBRARY_PATH": true, "NODE_OPTIONS": true, "PYTHONSTARTUP": true,
	"PYTHONPATH": true, "PERL5OPT": true, "PERL5LIB": true, "RUBYOPT": true, "JAVA_TOOL_OPTIONS": true,
	"_JAVA_OPTIONS": true, "JDK_JAVA_OPTIONS": true, "GIT_SSH_COMMAND": true, "GIT_EXEC_PATH": true,
	"GIT_CONFIG": true, "GIT_CONFIG_GLOBAL": true, "GIT_CONFIG_SYSTEM": true, "GIT_ASKPASS": true,
}

// isControlEnvName reports names whose author-set values deserve a warning.
func isControlEnvName(name string) bool {
	up := strings.ToUpper(name)
	return controlEnvNames[up] || strings.HasPrefix(up, "GIT_CONFIG_")
}

// sensitiveExact, sensitivePrefixes, and sensitiveSuffixes cover variables that
// select programs, configuration, directories, proxies, or CI runner behavior.
// None of them may take a value from ${git.branch}, which the branch author controls.
var (
	sensitiveExact = map[string]bool{
		"HOME": true, "CDPATH": true, "SHELL": true, "EDITOR": true, "VISUAL": true, "PAGER": true, "TMPDIR": true,
		"GLIBC_TUNABLES": true, "KUBECONFIG": true, "NODE_PATH": true, "PYTHONHOME": true, "RUBYLIB": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true, "FTP_PROXY": true,
		"CC": true, "CXX": true, "CPP": true, "LD": true, "AR": true, "AS": true, "MAKEFLAGS": true,
		"MFLAGS": true, "GNUMAKEFLAGS": true, "MAKE": true, "VIRTUAL_ENV": true,
	}
	sensitivePrefixes = []string{"LD_", "DYLD_", "BASH_", "GIT_", "CI_", "FF_", "DOCKER_", "XDG_", "NPM_CONFIG_",
		"PIP_", "PIPENV_", "SSL_", "CURL_", "GITHUB_", "RUNNER_", "ACTIONS_", "SYSTEM_", "AGENT_", "BUILD_", "BAMBOO_",
		"JENKINS_", "KUBE", "PYTHON", "PERL", "RUBY", "JAVA", "NODE_", "GRADLE_", "MAVEN_", "CARGO_", "GO",
		"UV_", "TF_", "AWS_", "GOOGLE_", "AZURE_", "ARM_", "BUNDLE_", "COMPOSER_", "RUSTC", "RUSTUP_", "CONDA",
		"POETRY_", "YARN_", "PNPM_", "DENO_", "HELM_", "ANSIBLE_", "VAULT_", "SSH_", "GPG", "GNUPG",
		"PYENV_", "RBENV_", "NODENV_", "NVM_", "ASDF_", "SDKMAN_", "COMPOSE_", "BUILDX_", "SONAR_", "REGISTRY"}
	sensitiveSuffixes = []string{"_OPTS", "_OPTIONS", "PATH", "_HOME", "_CONFIG", "_DIR", "_PROXY", "_COMMAND",
		"_FILE", "FLAGS", "_WRAPPER", "_ARGS", "_CMD", "_BIN", "_EXE", "_SHELL", "_PROGRAM",
		"_FILES", "_DIRS", "_PATHS", "_HOST", "_HOSTS", "_HOST_URL", "_SERVER", "_ENDPOINT"}
)

// neutralBranchNameRe is the allowlist for variables that may carry a branch name:
// names that describe the value rather than configure a tool.
var neutralBranchNameRe = regexp.MustCompile(`(?:^|_)(?:BRANCH|REF|TAG|VERSION|NAME|LABEL|ENV|ENVIRONMENT|CHANNEL|TARGET|URL|SLUG)(?:_|$)`)

// isSensitiveEnvName reports names that must never carry untrusted values.
func isSensitiveEnvName(name string) bool {
	up := strings.ToUpper(name)
	if isControlEnvName(up) || sensitiveExact[up] {
		return true
	}
	for _, p := range sensitivePrefixes {
		if strings.HasPrefix(up, p) {
			return true
		}
	}
	for _, s := range sensitiveSuffixes {
		if strings.HasSuffix(up, s) {
			return true
		}
	}
	return false
}

// checkControlEnv rejects untrusted values in interpreter-control variables and
// warns about author-set ones.
func (v *validator) checkControlEnv(path, name, value string) {
	t, err := ParseTemplate(value)
	if err == nil && slices.Contains(t.Refs(), Ref{Namespace: NSGit, Name: GitBranch}) &&
		(isSensitiveEnvName(name) || !neutralBranchNameRe.MatchString(strings.ToUpper(name))) {
		v.errorf(path, "%s cannot be set from ${git.branch}: only neutral names such as BRANCH_NAME or IMAGE_TAG may carry a branch name, because tools act on many variables", name).
			Hint = "use a name with one of the words BRANCH, REF, TAG, VERSION, NAME, LABEL, ENV, ENVIRONMENT, CHANNEL, TARGET, URL, or SLUG, such as DEPLOY_ENV"
		return
	}
	if !isControlEnvName(name) {
		return
	}
	v.add(diagnostics.Warningf(path, "%s changes how shells or tools behave and can run code outside the run steps", name).
		WithHint("set it inside the script where it is needed"))
}

func hasFold(set map[string]bool, name string) bool {
	for k := range set {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

// markerTextRe matches custom-block marker text; values containing it would make
// generated files look like they have malformed custom blocks.
var markerTextRe = regexp.MustCompile(`(?i)renvoy\s*:\s*(begin|end)\s*custom`)

func (v *validator) checkNoMarker(path, value string) {
	if markerTextRe.MatchString(value) {
		v.errorf(path, "values may not contain custom-block marker text (renvoy:begin / renvoy:end)")
	}
}

// checkNoMarkers applies checkNoMarker to every string in the pipeline, so no
// field can place a marker line into generated output.
func (v *validator) checkNoMarkers(val reflect.Value, path string) {
	switch val.Kind() {
	case reflect.Pointer:
		if !val.IsNil() {
			v.checkNoMarkers(val.Elem(), path)
		}
	case reflect.String:
		v.checkNoMarker(path, val.String())
	case reflect.Struct:
		t := val.Type()
		for i := 0; i < t.NumField(); i++ {
			tag := strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0]
			if tag == "" || tag == "-" || t.Field(i).Type == reflect.TypeOf(Extensions{}) {
				continue
			}
			v.checkNoMarkers(val.Field(i), JoinKey(path, tag))
		}
	case reflect.Slice:
		switch x := val.Interface().(type) {
		case Vars:
			for _, e := range x {
				v.checkNoMarker(JoinKey(path, e.Name), e.Value)
			}
			return
		case Matrix:
			for _, a := range x {
				for i, value := range a.Values {
					v.checkNoMarker(JoinIndex(JoinKey(path, a.Name), i), value)
				}
			}
			return
		}
		for i := 0; i < val.Len(); i++ {
			v.checkNoMarkers(val.Index(i), JoinIndex(path, i))
		}
	case reflect.Map:
		for _, k := range val.MapKeys() {
			v.checkNoMarker(JoinKey(path, k.String()), val.MapIndex(k).String())
		}
	}
}

// takenName reports whether name matches a variable or secret, ignoring case.
func (v *validator) takenName(name string) bool {
	for _, va := range v.p.Variables {
		if strings.EqualFold(va.Name, name) {
			return true
		}
	}
	for _, s := range v.p.Secrets {
		if strings.EqualFold(s, name) {
			return true
		}
	}
	return false
}

// checkTemplate validates references in s. allowed lists the permitted namespaces.
func (v *validator) checkTemplate(path, s string, axes map[string]bool, allowed ...string) {
	t, err := ParseTemplate(s)
	if err != nil {
		v.errorf(path, "%v", err).Hint = "write $${ for a literal ${"
		return
	}
	for _, r := range t.Refs() {
		if !slices.Contains(allowed, r.Namespace) {
			v.errorf(path, "%s is not allowed here", r).Hint = "allowed namespaces: " + strings.Join(allowed, ", ")
			continue
		}
		switch r.Namespace {
		case NSVariables:
			if _, ok := v.p.Variables.Get(r.Name); !ok {
				v.errorf(path, "%s refers to an undefined variable", r).Hint = "define it under variables"
			}
		case NSSecrets:
			if !slices.Contains(v.p.Secrets, r.Name) {
				v.errorf(path, "%s refers to an undeclared secret", r).Hint = "declare the secret name under secrets"
			}
		case NSMatrix:
			if !axes[r.Name] {
				v.errorf(path, "%s refers to a matrix axis this job does not define", r)
			}
		}
	}
}

func (v *validator) checkLiteral(path, s string) bool {
	t, err := ParseTemplate(s)
	if err != nil {
		v.errorf(path, "%v", err)
		return false
	}
	if !t.IsLiteral() {
		v.errorf(path, "references are not allowed here; found %s", t.Refs()[0])
		return false
	}
	return true
}

func (v *validator) checkRelPath(path, s string, required bool) {
	if s == "" {
		if required {
			v.errorf(path, "a path is required")
		}
		return
	}
	if !v.checkLiteral(path, s) {
		return
	}
	if err := ValidateRelPath(s); err != nil {
		v.errorf(path, "%v", err)
	}
}

// checkLiteralPath validates a required relative path that must not contain wildcards.
func (v *validator) checkLiteralPath(path, s string) {
	v.checkRelPath(path, s, true)
	if strings.ContainsAny(s, "*?[]{}") {
		v.errorf(path, "path %q must not contain wildcards", s).
			Hint = "list files or directories explicitly; end directories with /"
	}
}

// ValidateRelPath checks that s is a portable, workspace-relative path.
func ValidateRelPath(s string) error {
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("path %q must not start with '-', which tools read as an option", s)
	}
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") || strings.HasPrefix(s, "\\") ||
		(len(s) > 1 && s[1] == ':') {
		return fmt.Errorf("path %q must be relative to the workspace", s)
	}
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return fmt.Errorf("path %q must not contain '..'", s)
		}
	}
	if strings.HasPrefix(s, "!") || strings.HasPrefix(s, "#") || strings.Contains(s, "\\") || strings.TrimSpace(s) != s {
		// actions/upload-artifact and actions/cache read ! as an exclusion and # as
		// a comment, trim lines, and treat \ as a glob escape.
		return fmt.Errorf("path %q must not start with '!' or '#', contain '\\', or start or end with spaces; separate directories with '/'", s)
	}
	if strings.ContainsAny(s, "\n\r\t'\"`$,;|&<>") {
		return fmt.Errorf("path %q contains characters that are not portable across providers", s)
	}
	return nil
}

func (v *validator) checkStep(path string, s *Step, j *Job, axes map[string]bool) {
	kinds := s.Kinds()
	switch len(kinds) {
	case 0:
		v.errorf(path, "step does nothing").
			Hint = "set one of run, uses, cache, uploadArtifact, downloadArtifact, publishTestResults, buildImage, pushImage, deployKubernetes"
		return
	case 1:
	default:
		names := make([]string, len(kinds))
		for i, k := range kinds {
			names[i] = string(k)
			if k == StepCheckout {
				names[i] = "uses"
			}
		}
		v.errorf(path, "step sets more than one action (%s)", strings.Join(names, ", ")).Hint = "split it into separate steps"
		return
	}
	if strings.ContainsAny(s.Name, "\n\r") {
		v.errorf(path+".name", "step names must be a single line")
	}

	v.warnProviderSyntax(path+".name", s.Name)
	kind := kinds[0]
	v.checkCondition(path+".when", s.When)
	v.checkEnv(path+".env", s.Env, axes)
	if s.WorkingDirectory != "" {
		if kind != StepRun {
			v.errorf(path+".workingDirectory", "workingDirectory applies only to run steps")
		}
		v.checkRelPath(path+".workingDirectory", s.WorkingDirectory, false)
	}
	if s.Shell != "" {
		if kind != StepRun {
			v.errorf(path+".shell", "shell applies only to run steps")
		} else if s.Shell != ShellBash && s.Shell != ShellSh && s.Shell != ShellPwsh {
			v.errorf(path+".shell", "unknown shell %q", s.Shell).Hint = "use bash, sh, or pwsh"
		}
	}
	if len(s.With) > 0 && kind != StepUses {
		v.errorf(path+".with", "with applies only to uses steps that reference a reusable action or task")
	}
	inMatrix := len(j.Matrix) > 0

	switch kind {
	case StepRun:
		v.checkTemplate(path+".run", s.Run, axes, NSVariables, NSSecrets, NSGit, NSMatrix)
		if lit := literalText(s.Run); strings.Contains(lit, "${{") || providerVarRe.MatchString(lit) || azureMacroRe.MatchString(lit) {
			v.add(diagnostics.Warningf(path+".run", "run script contains provider-specific substitution syntax (${{ }}, ${bamboo.x}, or $(Name.Value)), which the provider expands into the script text").
				WithHint("use portable references such as ${secrets.NAME} or ${git.branch}, which reach scripts as environment variables"))
		}
	case StepUses:
		if !usesRefRe.MatchString(s.Uses) {
			v.errorf(path+".uses", "invalid reference %q", s.Uses)
		}
		for _, w := range s.With {
			wp := JoinKey(path+".with", w.Name)
			if !envNameRe.MatchString(strings.ReplaceAll(w.Name, "-", "_")) {
				v.errorf(wp, "invalid input name %q", w.Name)
			}
			v.checkTemplate(wp, w.Value, axes, NSVariables, NSSecrets, NSGit, NSMatrix)
			v.warnProviderSyntax(wp, w.Value)
			if t, err := ParseTemplate(w.Value); err == nil && slices.Contains(t.Refs(), Ref{Namespace: NSGit, Name: GitBranch}) {
				v.add(diagnostics.Warningf(wp, "${git.branch} is controlled by whoever names the branch and is passed to this step as-is").
					WithHint("only pass it to inputs that are treated as plain data, never to inputs evaluated as code or scripts"))
			}
		}
	case StepCache:
		c := s.Cache
		if !cacheKeyRe.MatchString(c.Key) {
			v.errorf(path+".cache.key", "cache key %q must be letters, digits, '.', '_' and '-'", c.Key)
		}
		if len(c.Paths) == 0 {
			v.errorf(path+".cache.paths", "at least one cache path is required")
		}
		for i, p := range c.Paths {
			v.checkLiteralPath(JoinIndex(path+".cache.paths", i), p)
		}
		for i, f := range c.Files {
			v.checkRelPath(JoinIndex(path+".cache.files", i), f, true)
		}
	case StepUploadArtifact:
		a := s.UploadArtifact
		if !artifactNameRe.MatchString(a.Name) {
			v.errorf(path+".uploadArtifact.name", "invalid artifact name %q", a.Name).
				Hint = "use letters, digits, '.', '_' and '-'"
		}
		if len(a.Paths) == 0 {
			v.errorf(path+".uploadArtifact.paths", "at least one artifact path is required")
		}
		for i, p := range a.Paths {
			v.checkLiteralPath(JoinIndex(path+".uploadArtifact.paths", i), p)
		}
		if inMatrix {
			v.errorf(path+".uploadArtifact", "artifacts cannot be uploaded from matrix jobs in %s", APIVersionV1Alpha1).
				Hint = "every matrix combination would upload the same artifact name"
		}
	case StepDownloadArtifact:
		a := s.DownloadArtifact
		if !artifactNameRe.MatchString(a.Name) {
			v.errorf(path+".downloadArtifact.name", "invalid artifact name %q", a.Name)
		}
	case StepPublishTestResults:
		t := s.PublishTestResults
		v.checkRelPath(path+".publishTestResults.path", t.Path, true)
		if t.Format != "" && t.Format != TestFormatJUnit {
			v.errorf(path+".publishTestResults.format", "unsupported test format %q", t.Format).Hint = "use junit"
		}
	case StepBuildImage:
		b := s.BuildImage
		v.checkImage(path+".buildImage.image", b.Image, axes)
		if b.Context != "" && b.Context != "." {
			v.checkRelPath(path+".buildImage.context", b.Context, false)
		}
		v.checkRelPath(path+".buildImage.dockerfile", b.Dockerfile, false)
		for _, a := range b.BuildArgs {
			ap := JoinKey(path+".buildImage.buildArgs", a.Name)
			v.checkEnvName(ap, a.Name, "build argument")
			v.checkTemplate(ap, a.Value, axes, NSVariables, NSGit, NSMatrix)
			// Docker applies predefined args such as HTTP_PROXY to every RUN, and
			// declared args become environment variables, so the env rules apply.
			v.checkControlEnv(ap, a.Name, a.Value)
		}
	case StepPushImage:
		v.checkImage(path+".pushImage.image", s.PushImage.Image, axes)
	case StepDeployKubernetes:
		d := s.DeployKubernetes
		v.checkRelPath(path+".deployKubernetes.manifests", d.Manifests, true)
		if d.Namespace != "" && !namespaceRe.MatchString(d.Namespace) {
			v.errorf(path+".deployKubernetes.namespace", "invalid Kubernetes namespace %q", d.Namespace)
		}
	}
}

func (v *validator) checkImage(path, image string, axes map[string]bool) {
	if image == "" {
		v.errorf(path, "image is required")
		return
	}
	v.checkTemplate(path, image, axes, NSVariables, NSGit, NSMatrix)
	v.checkExpandedImage(path, image)
}

// imageRefRe is the Docker image reference grammar: an optional registry host,
// lowercase path components, an optional tag, and an optional digest.
var imageRefRe = func() *regexp.Regexp {
	const (
		domainPart = `(?:[a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9-]*[a-zA-Z0-9])`
		domain     = `(?:` + domainPart + `(?:\.` + domainPart + `)*|\[[0-9A-Fa-f:]+\])(?::[0-9]+)?`
		pathPart   = `[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*`
		tag        = `[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}`
		digest     = `(?:sha256:[a-f0-9]{64}|sha384:[a-f0-9]{96}|sha512:[a-f0-9]{128})`
	)
	return regexp.MustCompile(`^(?:` + domain + `/)?` + pathPart + `(?:/` + pathPart + `)*(?::` + tag + `)?(?:@` + digest + `)?$`)
}()

// checkExpandedImage validates an image name with variable values substituted,
// because some providers inline variables into image fields.
func (v *validator) checkExpandedImage(path, image string) {
	t, err := ParseTemplate(image)
	if err != nil {
		return
	}
	var b strings.Builder
	for _, seg := range t {
		switch {
		case seg.Ref == nil:
			b.WriteString(seg.Literal)
		case seg.Ref.Namespace == NSVariables:
			val, _ := v.p.Variables.Get(seg.Ref.Name)
			b.WriteString(val)
		default:
			b.WriteString("x")
		}
	}
	if len(b.String()) > 255 || !imageRefRe.MatchString(b.String()) {
		v.errorf(path, "image %q is not a valid image reference once variables are substituted (%q)", image, b.String()).
			Hint = "use [registry/]lowercase/path[:tag][@sha256:digest], such as ghcr.io/acme/app:1.2"
	}
}
