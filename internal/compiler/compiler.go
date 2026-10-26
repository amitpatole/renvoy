// Package compiler resolves a validated pipeline into its normalized form: stage
// and job dependency graphs, topological order, and artifact data flow.
package compiler

import (
	"strings"

	"github.com/amitpatole/renvoy/internal/diagnostics"
	"github.com/amitpatole/renvoy/internal/pipeline"
)

// Normalize annotates p with resolved graph information and reports graph errors.
// It must run after pipeline.Validate reports no errors.
func Normalize(p *pipeline.Pipeline) diagnostics.List {
	var diags diagnostics.List
	// Variable values are literal; resolve $${ escapes once so providers render text.
	for i, v := range p.Variables {
		p.Variables[i].Value = pipeline.MustTemplate(v.Value).Literal()
	}
	stageByName := map[string]*pipeline.Stage{}
	for i, s := range p.Stages {
		s.Index = i
		stageByName[s.Name] = s
		for j, job := range s.Jobs {
			job.Stage, job.Index = s, j
			for k, step := range job.Steps {
				step.Job, step.Index = job, k
			}
		}
	}

	for i, s := range p.Stages {
		s.Needs = nil
		if s.DependsOn == nil {
			if i > 0 {
				s.Needs = []*pipeline.Stage{p.Stages[i-1]}
			}
			continue
		}
		seen := map[string]bool{}
		for k, name := range *s.DependsOn {
			path := pipeline.JoinIndex(s.Path()+".dependsOn", k)
			dep, ok := stageByName[name]
			switch {
			case !ok:
				diags = append(diags, diagnostics.Errorf(path, "stage %q depends on unknown stage %q", s.Name, name))
			case dep == s:
				diags = append(diags, diagnostics.Errorf(path, "stage %q depends on itself", s.Name))
			case seen[name]:
				diags = append(diags, diagnostics.Warningf(path, "stage %q lists dependency %q more than once", s.Name, name))
			default:
				s.Needs = append(s.Needs, dep)
			}
			seen[name] = true
		}
	}

	order, cycle := topoSort(p.Stages, func(s *pipeline.Stage) []*pipeline.Stage { return s.Needs })
	if cycle != nil {
		names := make([]string, len(cycle))
		for i, s := range cycle {
			names[i] = s.Name
		}
		diags = append(diags, diagnostics.Errorf(cycle[0].Path()+".dependsOn",
			"stage dependency cycle: %s", strings.Join(names, " -> ")))
		return diags
	}
	p.Order = order

	for _, s := range p.Stages {
		jobByName := map[string]*pipeline.Job{}
		for _, j := range s.Jobs {
			jobByName[j.Name] = j
		}
		for _, j := range s.Jobs {
			j.SameStageNeeds = nil
			seen := map[string]bool{}
			for k, name := range j.DependsOn {
				path := pipeline.JoinIndex(j.Path()+".dependsOn", k)
				dep, ok := jobByName[name]
				switch {
				case !ok:
					d := diagnostics.Errorf(path, "job %q depends on %q, which is not a job in stage %q", j.Name, name, s.Name)
					d = d.WithHint("jobs depend on jobs in the same stage; use stage dependsOn for cross-stage ordering")
					diags = append(diags, d)
				case dep == j:
					diags = append(diags, diagnostics.Errorf(path, "job %q depends on itself", j.Name))
				case seen[name]:
					diags = append(diags, diagnostics.Warningf(path, "job %q lists dependency %q more than once", j.Name, name))
				default:
					j.SameStageNeeds = append(j.SameStageNeeds, dep)
				}
				seen[name] = true
			}
		}
		jobOrder, cycle := topoSort(s.Jobs, func(j *pipeline.Job) []*pipeline.Job { return j.SameStageNeeds })
		if cycle != nil {
			names := make([]string, len(cycle))
			for i, j := range cycle {
				names[i] = j.Name
			}
			diags = append(diags, diagnostics.Errorf(cycle[0].Path()+".dependsOn",
				"job dependency cycle in stage %q: %s", s.Name, strings.Join(names, " -> ")))
			continue
		}
		s.JobOrder = jobOrder
	}
	if diags.HasErrors() {
		return diags
	}

	// A job with same-stage dependencies already waits for upstream stages through
	// them, so only a stage's entry jobs need edges to the needed stages.
	for _, s := range p.Stages {
		for _, j := range s.Jobs {
			j.Needs = append([]*pipeline.Job{}, j.SameStageNeeds...)
			if len(j.SameStageNeeds) > 0 {
				continue
			}
			for _, dep := range s.Needs {
				j.Needs = append(j.Needs, FinalJobs(dep)...)
			}
		}
	}
	// Ancestor sets are computed once, in topological order, so later per-job
	// queries are linear rather than re-walking the graph.
	for _, j := range p.OrderedJobs() {
		j.Ancestors = map[*pipeline.Job]bool{}
		for _, d := range j.Needs {
			j.Ancestors[d] = true
			for a := range d.Ancestors {
				j.Ancestors[a] = true
			}
		}
	}
	diags = append(diags, checkArtifacts(p)...)
	return diags
}

// FinalJobs returns the jobs of s that no other job in s depends on, in job order.
// Waiting for them is equivalent to waiting for the whole stage.
func FinalJobs(s *pipeline.Stage) []*pipeline.Job {
	depended := map[*pipeline.Job]bool{}
	for _, j := range s.Jobs {
		for _, d := range j.SameStageNeeds {
			depended[d] = true
		}
	}
	var out []*pipeline.Job
	for _, j := range s.JobOrder {
		if !depended[j] {
			out = append(out, j)
		}
	}
	return out
}

// Ancestors returns every job that must finish before j starts.
// It requires a normalized pipeline.
func Ancestors(j *pipeline.Job) map[*pipeline.Job]bool { return j.Ancestors }

// Artifact describes where an artifact is produced.
type Artifact struct {
	Name     string
	Producer *pipeline.Job
	Step     *pipeline.Step
}

// Artifacts maps artifact names to their single producer.
func Artifacts(p *pipeline.Pipeline) map[string]Artifact {
	out := map[string]Artifact{}
	for _, j := range p.AllJobs() {
		for _, s := range j.Steps {
			if s.UploadArtifact != nil {
				if _, dup := out[s.UploadArtifact.Name]; !dup {
					out[s.UploadArtifact.Name] = Artifact{Name: s.UploadArtifact.Name, Producer: j, Step: s}
				}
			}
		}
	}
	return out
}

func checkArtifacts(p *pipeline.Pipeline) diagnostics.List {
	var diags diagnostics.List
	producers := map[string]*pipeline.Step{}
	for _, j := range p.AllJobs() {
		for _, s := range j.Steps {
			if s.UploadArtifact == nil {
				continue
			}
			if first, dup := producers[s.UploadArtifact.Name]; dup {
				diags = append(diags, diagnostics.Errorf(s.Path()+".uploadArtifact.name",
					"artifact %q is already uploaded by job %q", s.UploadArtifact.Name, first.Job.Name))
				continue
			}
			producers[s.UploadArtifact.Name] = s
		}
	}
	for _, j := range p.AllJobs() {
		ancestors := Ancestors(j)
		for _, s := range j.Steps {
			if s.DownloadArtifact == nil {
				continue
			}
			path := s.Path() + ".downloadArtifact.name"
			prod, ok := producers[s.DownloadArtifact.Name]
			switch {
			case !ok:
				diags = append(diags, diagnostics.Errorf(path, "no job uploads artifact %q", s.DownloadArtifact.Name))
			case prod.Job == j:
				diags = append(diags, diagnostics.Errorf(path,
					"job %q downloads artifact %q that it uploads itself", j.Name, s.DownloadArtifact.Name))
			case !ancestors[prod.Job]:
				diags = append(diags, diagnostics.Errorf(path,
					"job %q downloads artifact %q, but its producer %q is not an upstream dependency",
					j.Name, s.DownloadArtifact.Name, prod.Job.Name).
					WithHint("make %q depend (directly or through its stage) on %q", j.Name, prod.Job.Name))
			}
		}
	}
	return diags
}

// topoSort orders nodes so dependencies come first, preferring declaration order.
// It returns a cycle (first node repeated at the end) when one exists.
func topoSort[T comparable](nodes []T, deps func(T) []T) ([]T, []T) {
	const (
		unvisited = iota
		visiting
		done
	)
	state := map[T]int{}
	var order []T
	var stack []T
	var cycle []T
	var visit func(T) bool
	visit = func(n T) bool {
		switch state[n] {
		case done:
			return true
		case visiting:
			for i, s := range stack {
				if s == n {
					cycle = append(append([]T{}, stack[i:]...), n)
					break
				}
			}
			return false
		}
		state[n] = visiting
		stack = append(stack, n)
		for _, d := range deps(n) {
			if !visit(d) {
				return false
			}
		}
		stack = stack[:len(stack)-1]
		state[n] = done
		order = append(order, n)
		return true
	}
	for _, n := range nodes {
		if !visit(n) {
			return nil, cycle
		}
	}
	return order, nil
}
