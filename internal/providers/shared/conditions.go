package shared

import (
	"path"
	"strings"

	"github.com/amitpatole/renvoy/internal/compiler"
	"github.com/amitpatole/renvoy/internal/pipeline"
)

// OwnConditions returns the non-empty stage and job conditions of j.
func OwnConditions(j *pipeline.Job) []*pipeline.Condition {
	var out []*pipeline.Condition
	if !j.Stage.When.IsEmpty() {
		out = append(out, j.Stage.When)
	}
	if !j.When.IsEmpty() {
		out = append(out, j.When)
	}
	return out
}

// InheritedConditions returns the conditions of j and of every upstream job, without
// duplicates. Providers whose skipped jobs do not skip dependents render these so a
// job runs only when all of its upstream jobs could have run.
func InheritedConditions(p *pipeline.Pipeline, j *pipeline.Job) []*pipeline.Condition {
	ancestors := compiler.Ancestors(j)
	var out []*pipeline.Condition
	seen := map[*pipeline.Condition]bool{}
	for _, x := range p.OrderedJobs() {
		if x != j && !ancestors[x] {
			continue
		}
		for _, c := range OwnConditions(x) {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// UploadRoot returns the directory an artifact's files are stored relative to by
// providers that root artifacts at the paths' common ancestor (GitHub, Azure).
// Paths ending in "/" are directories; others are files.
func UploadRoot(paths []string) string {
	var dirs []string
	for _, p := range paths {
		if strings.HasSuffix(p, "/") {
			dirs = append(dirs, path.Clean(p))
		} else {
			dirs = append(dirs, path.Dir(path.Clean(p)))
		}
	}
	root := dirs[0]
	for _, d := range dirs[1:] {
		for root != "." && d != root && !strings.HasPrefix(d, root+"/") {
			root = path.Dir(root)
		}
	}
	return root
}
