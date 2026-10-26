package providers

import (
	"github.com/amitpatole/renvoy/internal/diagnostics"
	"github.com/amitpatole/renvoy/internal/pipeline"
)

// Usage is one place a pipeline relies on a capability.
type Usage struct {
	Capability Capability
	Path       string
}

// Usages lists every capability a compiled pipeline relies on, with config paths.
func Usages(p *pipeline.Pipeline) []Usage {
	var u []Usage
	add := func(c Capability, path string) { u = append(u, Usage{c, path}) }
	t := p.Triggers
	if t.Push != nil {
		add(CapPushTrigger, "triggers.push")
		if len(t.Push.Branches) > 0 {
			add(CapBranchFilters, "triggers.push.branches")
		}
		if len(t.Push.Tags) > 0 {
			add(CapTagTrigger, "triggers.push.tags")
		}
	}
	if t.PullRequest != nil {
		add(CapPullRequestTrigger, "triggers.pullRequest")
		if len(t.PullRequest.Branches) > 0 {
			add(CapBranchFilters, "triggers.pullRequest.branches")
		}
	}
	if t.Manual != nil {
		add(CapManualTrigger, "triggers.manual")
	}
	for i, s := range t.Schedules {
		path := pipeline.JoinIndex("triggers.schedules", i)
		add(CapScheduleTrigger, path)
		if len(s.Branches) > 0 {
			add(CapBranchFilters, path+".branches")
		}
	}
	if len(p.Variables) > 0 {
		add(CapVariables, "variables")
	}
	if len(p.Secrets) > 0 {
		add(CapSecrets, "secrets")
	}
	for _, s := range p.Stages {
		if s.DependsOn != nil {
			add(CapStageGraph, s.Path()+".dependsOn")
		}
		if !s.When.IsEmpty() {
			add(CapConditions, s.Path()+".when")
		}
		for _, j := range s.Jobs {
			jp := j.Path()
			if len(j.DependsOn) > 0 {
				add(CapJobGraph, jp+".dependsOn")
			}
			if !j.When.IsEmpty() {
				add(CapConditions, jp+".when")
			}
			switch j.OS() {
			case pipeline.OSWindows:
				add(CapRunnerWindows, jp+".runsOn")
			case pipeline.OSMacOS:
				add(CapRunnerMacOS, jp+".runsOn")
			default:
				add(CapRunnerLinux, jp+".runsOn")
			}
			if j.Container != nil {
				add(CapContainer, jp+".container")
				if j.Container.Options != "" {
					add(CapContainerOptions, jp+".container.options")
				}
			}
			if len(j.Matrix) > 0 {
				add(CapMatrix, jp+".matrix")
			}
			if j.TimeoutMinutes > 0 {
				add(CapTimeout, jp+".timeoutMinutes")
			}
			if j.Retries > 0 {
				add(CapRetries, jp+".retries")
			}
			if j.Environment != nil {
				add(CapEnvironments, jp+".environment")
				if j.Environment.RequiresApproval() {
					add(CapApprovals, jp+".environment.approvals")
				}
			}
			for _, st := range j.Steps {
				sp := st.Path()
				if !st.When.IsEmpty() {
					add(CapStepConditions, sp+".when")
				}
				if len(st.Env) > 0 {
					add(CapStepEnv, sp+".env")
				}
				if st.WorkingDirectory != "" {
					add(CapWorkingDirectory, sp+".workingDirectory")
				}
				switch st.Kind() {
				case pipeline.StepCheckout:
					add(CapCheckout, sp+".uses")
				case pipeline.StepUses:
					add(CapReusableSteps, sp+".uses")
				case pipeline.StepCache:
					add(CapCache, sp+".cache")
				case pipeline.StepUploadArtifact:
					add(CapArtifactUpload, sp+".uploadArtifact")
				case pipeline.StepDownloadArtifact:
					add(CapArtifactDownload, sp+".downloadArtifact")
				case pipeline.StepPublishTestResults:
					add(CapTestResults, sp+".publishTestResults")
				case pipeline.StepBuildImage:
					add(CapImageBuild, sp+".buildImage")
				case pipeline.StepPushImage:
					add(CapImagePush, sp+".pushImage")
				case pipeline.StepDeployKubernetes:
					add(CapKubernetesDeploy, sp+".deployKubernetes")
				}
			}
		}
	}
	return u
}

// CheckCapabilities reports an error for every use of an unsupported capability and
// an informational note for each provider-managed capability (once per capability).
func CheckCapabilities(p *pipeline.Pipeline, prov Provider) diagnostics.List {
	var diags diagnostics.List
	provider, caps := prov.Name(), prov.Capabilities()
	noted := map[Capability]bool{}
	for _, u := range Usages(p) {
		info := caps[u.Capability]
		switch info.Support {
		case Unsupported:
			d := diagnostics.Errorf(u.Path, "%s cannot express %s", prov.DisplayName(), u.Capability)
			if info.Note != "" {
				d.Hint = info.Note
			}
			diags = append(diags, d.WithProvider(provider))
		case ProviderManaged:
			if !noted[u.Capability] {
				noted[u.Capability] = true
				diags = append(diags, diagnostics.Infof(u.Path, "%s is configured outside the generated file: %s",
					u.Capability, info.Note).WithProvider(provider))
			}
		}
	}
	return diags
}
