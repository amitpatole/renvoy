package pipeline

import (
	"fmt"
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
	v.checkName("metadata.name", p.Metadata.Name, "pipeline")
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
}

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
	for i, s := range t.Schedules {
		path := JoinIndex("triggers.schedules", i)
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

func (v *validator) checkPatterns(path string, pats []string) {
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
		for _, part := range strings.FieldsFunc(f, func(r rune) bool { return r == ',' || r == '-' || r == '/' || r == '*' }) {
			n, err := strconv.Atoi(part)
			if err != nil || n < 0 || n > 59 {
				return fmt.Errorf("cron %s field %q is out of range", names[i], f)
			}
			if !strings.Contains(f, "/") && (n < limits[i][0] || n > limits[i][1]) {
				return fmt.Errorf("cron %s value %d is outside %d-%d", names[i], n, limits[i][0], limits[i][1])
			}
		}
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
		}
		if j.OS() == OSWindows || j.OS() == OSMacOS {
			v.errorf(cp, "job containers require a linux runner")
		}
		if strings.ContainsAny(j.Container.Options, "\n\r") {
			v.errorf(cp+".options", "container options must be a single line")
		}
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
	combos := 1
	for _, axis := range m {
		ap := JoinKey(path, axis.Name)
		v.checkEnvName(ap, axis.Name, "matrix axis")
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

var matrixValueRe = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,64}$`)

func (v *validator) checkEnv(path string, env Vars, axes map[string]bool) {
	for _, e := range env {
		ep := JoinKey(path, e.Name)
		v.checkEnvName(ep, e.Name, "environment variable")
		if v.takenName(e.Name) || hasFold(axes, e.Name) {
			v.errorf(ep, "environment variable %q shadows a variable, secret, or matrix axis of the same name", e.Name).
				Hint = "rename it; shadowing would change what ${...} references resolve to on some providers"
		}
		v.checkTemplate(ep, e.Value, axes, NSVariables, NSSecrets, NSGit, NSMatrix)
	}
}

func hasFold(set map[string]bool, name string) bool {
	for k := range set {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
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
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") || strings.HasPrefix(s, "\\") ||
		(len(s) > 1 && s[1] == ':') {
		return fmt.Errorf("path %q must be relative to the workspace", s)
	}
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return fmt.Errorf("path %q must not contain '..'", s)
		}
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
		if strings.Contains(s.Run, "${{") {
			v.add(diagnostics.Warningf(path+".run", "run script contains provider-specific ${{ }} expression syntax").
				WithHint("use portable references such as ${secrets.NAME} or ${git.sha}"))
		}
	case StepUses:
		if !usesRefRe.MatchString(s.Uses) {
			v.errorf(path+".uses", "invalid reference %q", s.Uses)
		}
		for _, w := range s.With {
			if !envNameRe.MatchString(strings.ReplaceAll(w.Name, "-", "_")) {
				v.errorf(JoinKey(path+".with", w.Name), "invalid input name %q", w.Name)
			}
			v.checkTemplate(JoinKey(path+".with", w.Name), w.Value, axes, NSVariables, NSSecrets, NSGit, NSMatrix)
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

var imageCharsRe = regexp.MustCompile(`^[A-Za-z0-9._/:@-]+$`)

func (v *validator) checkImage(path, image string, axes map[string]bool) {
	if image == "" {
		v.errorf(path, "image is required")
		return
	}
	v.checkTemplate(path, image, axes, NSVariables, NSGit, NSMatrix)
	t, err := ParseTemplate(image)
	if err != nil {
		return
	}
	for _, seg := range t {
		if seg.Ref == nil && !imageCharsRe.MatchString(seg.Literal) {
			v.errorf(path, "image %q contains characters that are not valid in an image reference", image)
			return
		}
	}
}
