// Package providers defines the contract every CI/CD target implements and the
// capability model used to diagnose portability gaps.
package providers

import (
	"context"

	"github.com/amitpatole/renvoy/internal/diagnostics"
	"github.com/amitpatole/renvoy/internal/pipeline"
)

// Provider compiles a normalized pipeline into native files for one CI/CD system.
type Provider interface {
	// Name is the stable identifier used on the command line and as the extension namespace.
	Name() string
	// DisplayName is the human-readable product name used in diagnostics.
	DisplayName() string
	// Description is a one-line summary for `renvoy providers`.
	Description() string
	// Capabilities declares how each portable concept maps to this provider.
	Capabilities() CapabilitySet
	// Validate reports everything this provider cannot express faithfully.
	Validate(p *pipeline.Pipeline) diagnostics.List
	// Render produces native files. It is only called when Validate reported no errors.
	Render(ctx context.Context, p *pipeline.Pipeline, opts RenderOptions) ([]GeneratedFile, error)
}

// RenderOptions configures rendering.
type RenderOptions struct {
	// SourceName is the config file name recorded in generated headers.
	SourceName string
}

// Comment styles for generated files.
const (
	CommentHash  = "#"
	CommentSlash = "//"
	CommentXML   = "<!--"
	// CommentXMLEnd closes an XML comment line.
	CommentXMLEnd = " -->"
)

// GeneratedFile is one provider output before header, checksum, and custom-block handling.
type GeneratedFile struct {
	// Path is relative to the output directory and uses forward slashes.
	Path string
	// Content is the rendered body without the generated header.
	Content []byte
	// Comment is the line-comment prefix of the file format.
	Comment string
	// CommentEnd closes a comment line in formats without line comments (" -->").
	CommentEnd string
	// YAML marks content that must remain valid YAML after custom blocks are spliced in.
	YAML bool
}

// Support describes how a provider handles a portable concept.
type Support string

// Support levels.
const (
	// Native means the provider has a direct equivalent.
	Native Support = "native"
	// Emulated means Renvoy renders equivalent behavior with other constructs.
	Emulated Support = "emulated"
	// ProviderManaged means the behavior exists but is configured outside the generated file.
	ProviderManaged Support = "provider-managed"
	// Unsupported means the provider cannot express the concept; using it is an error.
	Unsupported Support = "unsupported"
)

// Capability names a portable concept.
type Capability string

// Portable capabilities, in display order.
const (
	CapPushTrigger        Capability = "triggers.push"
	CapTagTrigger         Capability = "triggers.tags"
	CapPullRequestTrigger Capability = "triggers.pullRequest"
	CapManualTrigger      Capability = "triggers.manual"
	CapScheduleTrigger    Capability = "triggers.schedules"
	CapBranchFilters      Capability = "triggers.branchFilters"
	CapStageGraph         Capability = "stages.dependsOn"
	CapJobGraph           Capability = "jobs.dependsOn"
	CapConditions         Capability = "jobs.when"
	CapStepConditions     Capability = "steps.when"
	CapRunnerLinux        Capability = "runners.linux"
	CapRunnerWindows      Capability = "runners.windows"
	CapRunnerMacOS        Capability = "runners.macos"
	CapContainer          Capability = "jobs.container"
	CapContainerOptions   Capability = "jobs.container.options"
	CapMatrix             Capability = "jobs.matrix"
	CapTimeout            Capability = "jobs.timeoutMinutes"
	CapRetries            Capability = "jobs.retries"
	CapVariables          Capability = "variables"
	CapSecrets            Capability = "secrets"
	CapStepEnv            Capability = "steps.env"
	CapWorkingDirectory   Capability = "steps.workingDirectory"
	CapCheckout           Capability = "steps.checkout"
	CapReusableSteps      Capability = "steps.uses"
	CapCache              Capability = "steps.cache"
	CapArtifactUpload     Capability = "steps.uploadArtifact"
	CapArtifactDownload   Capability = "steps.downloadArtifact"
	CapTestResults        Capability = "steps.publishTestResults"
	CapImageBuild         Capability = "steps.buildImage"
	CapImagePush          Capability = "steps.pushImage"
	CapKubernetesDeploy   Capability = "steps.deployKubernetes"
	CapEnvironments       Capability = "jobs.environment"
	CapApprovals          Capability = "jobs.environment.approvals"
)

// AllCapabilities lists every capability in display order.
var AllCapabilities = []Capability{
	CapPushTrigger, CapTagTrigger, CapPullRequestTrigger, CapManualTrigger, CapScheduleTrigger, CapBranchFilters,
	CapStageGraph, CapJobGraph, CapConditions, CapStepConditions,
	CapRunnerLinux, CapRunnerWindows, CapRunnerMacOS, CapContainer, CapContainerOptions, CapMatrix, CapTimeout, CapRetries,
	CapVariables, CapSecrets, CapStepEnv, CapWorkingDirectory,
	CapCheckout, CapReusableSteps, CapCache, CapArtifactUpload, CapArtifactDownload, CapTestResults,
	CapImageBuild, CapImagePush, CapKubernetesDeploy, CapEnvironments, CapApprovals,
}

// CapabilityInfo is one row of a provider's capability matrix.
type CapabilityInfo struct {
	ID      Capability `json:"id"`
	Support Support    `json:"support"`
	Note    string     `json:"note,omitempty"`
}

// CapabilitySet maps every capability to its support level.
type CapabilitySet map[Capability]CapabilityInfo

// NewCapabilitySet builds a set from entries. Capabilities not listed are Native.
func NewCapabilitySet(entries ...CapabilityInfo) CapabilitySet {
	set := CapabilitySet{}
	for _, c := range AllCapabilities {
		set[c] = CapabilityInfo{ID: c, Support: Native}
	}
	for _, e := range entries {
		set[e.ID] = e
	}
	return set
}

// Ordered returns the set in display order.
func (s CapabilitySet) Ordered() []CapabilityInfo {
	out := make([]CapabilityInfo, 0, len(AllCapabilities))
	for _, c := range AllCapabilities {
		if info, ok := s[c]; ok {
			out = append(out, info)
		}
	}
	return out
}
