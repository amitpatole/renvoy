// Package pipeline defines Renvoy's provider-neutral pipeline model (the IR),
// its YAML decoding, reference templates, and field-level semantic validation.
//
// This package must never import a provider package.
package pipeline

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

const (
	// APIVersionV1Alpha1 is the only supported apiVersion.
	APIVersionV1Alpha1 = "renvoy.dev/v1alpha1"
	// KindPipeline is the only supported kind.
	KindPipeline = "Pipeline"
)

// Pipeline is the root of a renvoy.yaml document.
type Pipeline struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Metadata   Metadata `yaml:"metadata"`
	Triggers   Triggers `yaml:"triggers"`
	// Variables hold literal values; after compilation $${ escapes are resolved.
	Variables  Vars       `yaml:"variables"`
	Secrets    []string   `yaml:"secrets"`
	Stages     []*Stage   `yaml:"stages"`
	Extensions Extensions `yaml:"extensions"`

	// Order lists stages in a stable topological order. Set by the compiler.
	Order []*Stage `yaml:"-"`
}

// Metadata identifies the pipeline.
type Metadata struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Labels      map[string]string `yaml:"labels"`
}

// Triggers declares which events start the pipeline.
type Triggers struct {
	Push        *PushTrigger        `yaml:"push"`
	PullRequest *PullRequestTrigger `yaml:"pullRequest"`
	Manual      *ManualTrigger      `yaml:"manual"`
	Schedules   []Schedule          `yaml:"schedules"`
}

// PushTrigger starts the pipeline on pushes to matching branches or tags.
type PushTrigger struct {
	Branches []string `yaml:"branches"`
	Tags     []string `yaml:"tags"`
}

// PullRequestTrigger starts the pipeline for pull/merge requests targeting matching branches.
type PullRequestTrigger struct {
	Branches []string `yaml:"branches"`
}

// ManualTrigger allows starting the pipeline by hand.
type ManualTrigger struct{}

// Schedule starts the pipeline on a five-field cron expression (UTC) for each listed branch.
type Schedule struct {
	Cron     string   `yaml:"cron"`
	Branches []string `yaml:"branches"`
}

// Stage groups jobs. A stage without dependsOn depends on the previously declared stage.
type Stage struct {
	Name      string     `yaml:"name"`
	DependsOn *[]string  `yaml:"dependsOn"`
	When      *Condition `yaml:"when"`
	Jobs      []*Job     `yaml:"jobs"`

	// Index is the declaration index. Set by the compiler.
	Index int `yaml:"-"`
	// Needs holds resolved direct stage dependencies. Set by the compiler.
	Needs []*Stage `yaml:"-"`
	// JobOrder lists jobs in a stable topological order. Set by the compiler.
	JobOrder []*Job `yaml:"-"`
}

// Path returns the config path of the stage, e.g. "stages[1]".
func (s *Stage) Path() string { return fmt.Sprintf("stages[%d]", s.Index) }

// Job is a unit of work executed on one runner.
type Job struct {
	Name           string       `yaml:"name"`
	DependsOn      []string     `yaml:"dependsOn"`
	RunsOn         string       `yaml:"runsOn"`
	Container      *Container   `yaml:"container"`
	Environment    *Environment `yaml:"environment"`
	Env            Vars         `yaml:"env"`
	When           *Condition   `yaml:"when"`
	TimeoutMinutes int          `yaml:"timeoutMinutes"`
	Retries        int          `yaml:"retries"`
	Matrix         Matrix       `yaml:"matrix"`
	Steps          []*Step      `yaml:"steps"`
	Extensions     Extensions   `yaml:"extensions"`

	// Stage is the owning stage. Set by the compiler.
	Stage *Stage `yaml:"-"`
	// Index is the declaration index within the stage. Set by the compiler.
	Index int `yaml:"-"`
	// SameStageNeeds holds resolved dependsOn jobs. Set by the compiler.
	SameStageNeeds []*Job `yaml:"-"`
	// Needs holds the direct job dependencies: same-stage needs, or for a stage's
	// entry jobs, the final jobs of each needed stage. Set by the compiler.
	Needs []*Job `yaml:"-"`
}

// Path returns the config path of the job, e.g. "stages[0].jobs[2]".
func (j *Job) Path() string { return fmt.Sprintf("%s.jobs[%d]", j.Stage.Path(), j.Index) }

// OS returns the runner operating system, defaulting to linux.
func (j *Job) OS() string {
	if j.RunsOn == "" {
		return OSLinux
	}
	return j.RunsOn
}

// Runner operating systems.
const (
	OSLinux   = "linux"
	OSWindows = "windows"
	OSMacOS   = "macos"
)

// Container runs every step of a job inside an image.
type Container struct {
	Image   string `yaml:"image"`
	Options string `yaml:"options"`
}

// Environment names a deployment target. It accepts a bare string as shorthand for {name: ...}.
type Environment struct {
	Name      string     `yaml:"name"`
	URL       string     `yaml:"url"`
	Approvals *Approvals `yaml:"approvals"`
}

// UnmarshalYAML accepts either a scalar name or a mapping.
func (e *Environment) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		e.Name = n.Value
		return nil
	}
	type plain Environment
	return n.Decode((*plain)(e))
}

// Approvals declares whether a human must approve deployments to the environment.
type Approvals struct {
	Required bool `yaml:"required"`
}

// RequiresApproval reports whether the environment demands approval.
func (e *Environment) RequiresApproval() bool {
	return e != nil && e.Approvals != nil && e.Approvals.Required
}

// Condition restricts when a stage, job, or step runs. All present fields must match.
type Condition struct {
	Branches []string `yaml:"branches"`
	Events   []string `yaml:"events"`
}

// IsEmpty reports whether the condition restricts nothing.
func (c *Condition) IsEmpty() bool {
	return c == nil || (len(c.Branches) == 0 && len(c.Events) == 0)
}

// Condition events.
const (
	EventPush        = "push"
	EventPullRequest = "pullRequest"
	EventManual      = "manual"
	EventSchedule    = "schedule"
)

// Events lists the valid condition events in canonical order.
var Events = []string{EventPush, EventPullRequest, EventManual, EventSchedule}

// MatrixAxis is one dimension of a job matrix.
type MatrixAxis struct {
	Name   string
	Values []string
}

// Matrix is an ordered set of axes; the job runs once per combination.
type Matrix []MatrixAxis

// UnmarshalYAML decodes a mapping of axis name to a list of scalar values, keeping order.
func (m *Matrix) UnmarshalYAML(n *yaml.Node) error {
	n = resolveAlias(n)
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("matrix must be a mapping")
	}
	out := Matrix{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], resolveAlias(n.Content[i+1])
		axis := MatrixAxis{Name: k.Value}
		for _, item := range v.Content {
			axis.Values = append(axis.Values, resolveAlias(item).Value)
		}
		out = append(out, axis)
	}
	*m = out
	return nil
}

// Combinations returns every combination of axis values in axis order.
func (m Matrix) Combinations() [][]string {
	if len(m) == 0 {
		return nil
	}
	combos := [][]string{{}}
	for _, axis := range m {
		var next [][]string
		for _, c := range combos {
			for _, v := range axis.Values {
				nc := append(append([]string{}, c...), v)
				next = append(next, nc)
			}
		}
		combos = next
	}
	return combos
}

// Step is one action within a job. Exactly one step kind field must be set.
type Step struct {
	Name             string     `yaml:"name"`
	Env              Vars       `yaml:"env"`
	WorkingDirectory string     `yaml:"workingDirectory"`
	When             *Condition `yaml:"when"`

	Uses               string                `yaml:"uses"`
	With               Vars                  `yaml:"with"`
	Run                string                `yaml:"run"`
	Shell              string                `yaml:"shell"`
	Cache              *CacheStep            `yaml:"cache"`
	UploadArtifact     *UploadArtifactStep   `yaml:"uploadArtifact"`
	DownloadArtifact   *DownloadArtifactStep `yaml:"downloadArtifact"`
	PublishTestResults *TestResultsStep      `yaml:"publishTestResults"`
	BuildImage         *BuildImageStep       `yaml:"buildImage"`
	PushImage          *PushImageStep        `yaml:"pushImage"`
	DeployKubernetes   *DeployKubernetesStep `yaml:"deployKubernetes"`

	// Job is the owning job. Set by the compiler.
	Job *Job `yaml:"-"`
	// Index is the position within the job. Set by the compiler.
	Index int `yaml:"-"`
}

// Path returns the config path of the step.
func (s *Step) Path() string { return fmt.Sprintf("%s.steps[%d]", s.Job.Path(), s.Index) }

// StepKind identifies what a step does.
type StepKind string

// Step kinds.
const (
	StepUnknown            StepKind = ""
	StepCheckout           StepKind = "checkout"
	StepUses               StepKind = "uses"
	StepRun                StepKind = "run"
	StepCache              StepKind = "cache"
	StepUploadArtifact     StepKind = "uploadArtifact"
	StepDownloadArtifact   StepKind = "downloadArtifact"
	StepPublishTestResults StepKind = "publishTestResults"
	StepBuildImage         StepKind = "buildImage"
	StepPushImage          StepKind = "pushImage"
	StepDeployKubernetes   StepKind = "deployKubernetes"
)

// BuiltinCheckout is the portable checkout reference.
const BuiltinCheckout = "checkout"

// Kinds returns every step kind field that is set, in declaration order.
func (s *Step) Kinds() []StepKind {
	var kinds []StepKind
	if s.Uses != "" {
		if s.Uses == BuiltinCheckout {
			kinds = append(kinds, StepCheckout)
		} else {
			kinds = append(kinds, StepUses)
		}
	}
	if s.Run != "" {
		kinds = append(kinds, StepRun)
	}
	if s.Cache != nil {
		kinds = append(kinds, StepCache)
	}
	if s.UploadArtifact != nil {
		kinds = append(kinds, StepUploadArtifact)
	}
	if s.DownloadArtifact != nil {
		kinds = append(kinds, StepDownloadArtifact)
	}
	if s.PublishTestResults != nil {
		kinds = append(kinds, StepPublishTestResults)
	}
	if s.BuildImage != nil {
		kinds = append(kinds, StepBuildImage)
	}
	if s.PushImage != nil {
		kinds = append(kinds, StepPushImage)
	}
	if s.DeployKubernetes != nil {
		kinds = append(kinds, StepDeployKubernetes)
	}
	return kinds
}

// Kind returns the single step kind, or StepUnknown if zero or several are set.
func (s *Step) Kind() StepKind {
	k := s.Kinds()
	if len(k) != 1 {
		return StepUnknown
	}
	return k[0]
}

// ShellFor returns the effective shell for a run-like step on the job's OS.
func (s *Step) ShellFor(os string) string {
	if s.Shell != "" {
		return s.Shell
	}
	if os == OSWindows {
		return ShellPwsh
	}
	return ShellBash
}

// Shells.
const (
	ShellBash = "bash"
	ShellSh   = "sh"
	ShellPwsh = "pwsh"
)

// CacheStep restores a cache before later steps and saves it when the job succeeds.
type CacheStep struct {
	Key   string   `yaml:"key"`
	Files []string `yaml:"files"`
	Paths []string `yaml:"paths"`
}

// UploadArtifactStep publishes files for later jobs or download. Paths are literal
// workspace-relative files; directories end with "/".
type UploadArtifactStep struct {
	Name  string   `yaml:"name"`
	Paths []string `yaml:"paths"`
}

// DownloadArtifactStep restores an artifact uploaded by an upstream job at its original paths.
type DownloadArtifactStep struct {
	Name string `yaml:"name"`
}

// TestResultsStep publishes test reports. It runs even when earlier steps fail.
type TestResultsStep struct {
	Path   string `yaml:"path"`
	Format string `yaml:"format"`
}

// TestFormatJUnit is the only supported test report format.
const TestFormatJUnit = "junit"

// BuildImageStep builds a container image with the docker CLI.
type BuildImageStep struct {
	Context    string `yaml:"context"`
	Dockerfile string `yaml:"dockerfile"`
	Image      string `yaml:"image"`
	BuildArgs  Vars   `yaml:"buildArgs"`
}

// PushImageStep pushes a container image with the docker CLI.
type PushImageStep struct {
	Image string `yaml:"image"`
}

// DeployKubernetesStep applies manifests with kubectl.
type DeployKubernetesStep struct {
	Manifests string `yaml:"manifests"`
	Namespace string `yaml:"namespace"`
}

// Var is one ordered name/value pair.
type Var struct {
	Name  string
	Value string
}

// Vars is an ordered string mapping that preserves declaration order.
type Vars []Var

// UnmarshalYAML decodes a mapping of scalars, preserving order.
func (v *Vars) UnmarshalYAML(n *yaml.Node) error {
	n = resolveAlias(n)
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("expected a mapping")
	}
	out := Vars{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, Var{Name: n.Content[i].Value, Value: resolveAlias(n.Content[i+1]).Value})
	}
	*v = out
	return nil
}

// Get returns the value of a named entry.
func (v Vars) Get(name string) (string, bool) {
	for _, e := range v {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

// Extensions holds provider-namespaced configuration as raw YAML, decoded by each provider.
type Extensions []Extension

// Extension is one provider namespace block.
type Extension struct {
	Namespace string
	Node      *yaml.Node
}

// UnmarshalYAML keeps each namespace as an undecoded node.
func (e *Extensions) UnmarshalYAML(n *yaml.Node) error {
	n = resolveAlias(n)
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("extensions must be a mapping")
	}
	out := Extensions{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, Extension{Namespace: n.Content[i].Value, Node: n.Content[i+1]})
	}
	*e = out
	return nil
}

// Get returns the extension node for a namespace, or nil.
func (e Extensions) Get(namespace string) *yaml.Node {
	for _, x := range e {
		if x.Namespace == namespace {
			return x.Node
		}
	}
	return nil
}

func resolveAlias(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

// AllJobs returns every job in declaration order.
func (p *Pipeline) AllJobs() []*Job {
	var jobs []*Job
	for _, s := range p.Stages {
		jobs = append(jobs, s.Jobs...)
	}
	return jobs
}

// OrderedJobs returns every job in stable topological order. Requires a compiled pipeline.
func (p *Pipeline) OrderedJobs() []*Job {
	var jobs []*Job
	for _, s := range p.Order {
		jobs = append(jobs, s.JobOrder...)
	}
	return jobs
}
