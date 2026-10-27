package shared

import (
	"regexp"
	"strings"

	"github.com/amitpatole/renvoy/internal/pipeline"
)

// EnvName maps a reference to the environment variable that carries it at runtime.
type EnvName func(pipeline.Ref) string

// ShellRef renders an environment variable expansion for the shell.
func ShellRef(shell, name string) string {
	if shell == pipeline.ShellPwsh {
		return "${env:" + name + "}"
	}
	return "${" + name + "}"
}

// Script renders a run script, replacing references with environment variable
// expansions. Literal text is unchanged, so references follow shell quoting rules.
func Script(t pipeline.Template, shell string, name EnvName) string {
	var b strings.Builder
	for _, s := range t {
		if s.Ref != nil {
			b.WriteString(ShellRef(shell, name(*s.Ref)))
		} else {
			b.WriteString(s.Literal)
		}
	}
	return b.String()
}

var safeWordRe = regexp.MustCompile(`^[A-Za-z0-9._/:@=+-]+$`)

// Word renders a template as one shell word, quoting when needed.
func Word(t pipeline.Template, shell string, name EnvName) string {
	if t.IsLiteral() && safeWordRe.MatchString(t.Literal()) && !strings.HasPrefix(t.Literal(), "-") &&
		shell != pipeline.ShellPwsh {
		return t.Literal()
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, s := range t {
		if s.Ref != nil {
			b.WriteString(ShellRef(shell, name(*s.Ref)))
			continue
		}
		for _, r := range s.Literal {
			if shell == pipeline.ShellPwsh {
				// PowerShell also ends double-quoted strings at typographic quotes.
				if r == '`' || r == '"' || r == '$' || r == '\u201c' || r == '\u201d' || r == '\u201e' {
					b.WriteByte('`')
				}
			} else if r == '\\' || r == '"' || r == '$' || r == '`' {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// PwshStrict starts PowerShell run scripts.
const PwshStrict = "$ErrorActionPreference = 'Stop'\n$PSNativeCommandUseErrorActionPreference = $true\n"

// Command returns the shell script a run or builtin command step executes, or
// "" for step kinds that are not shell commands.
func Command(s *pipeline.Step, shell string, name EnvName) string {
	word := func(v string) string { return Word(pipeline.MustTemplate(v), shell, name) }
	switch s.Kind() {
	case pipeline.StepRun:
		script := Script(pipeline.MustTemplate(s.Run), shell, name)
		if shell == pipeline.ShellPwsh {
			// Stop at the first failing cmdlet or native command (PowerShell 7.3+),
			// as bash -e does, instead of running the rest of the script.
			script = PwshStrict + script
		}
		return script
	case pipeline.StepBuildImage:
		b := s.BuildImage
		args := []string{"docker", "build"}
		if b.Dockerfile != "" {
			args = append(args, "--file", word(b.Dockerfile))
		}
		args = append(args, "--tag", word(b.Image))
		for _, a := range b.BuildArgs {
			args = append(args, "--build-arg", word(a.Name+"="+a.Value))
		}
		ctx := b.Context
		if ctx == "" {
			ctx = "."
		}
		args = append(args, word(ctx))
		return strings.Join(args, " ")
	case pipeline.StepPushImage:
		return "docker push " + word(s.PushImage.Image)
	case pipeline.StepDeployKubernetes:
		d := s.DeployKubernetes
		cmd := "kubectl apply --recursive --filename " + word(d.Manifests)
		if d.Namespace != "" {
			cmd += " --namespace " + word(d.Namespace)
		}
		return cmd
	}
	return ""
}

// CommandRefs returns the distinct references used by a step's shell command, in order.
func CommandRefs(s *pipeline.Step) []pipeline.Ref {
	var texts []string
	switch s.Kind() {
	case pipeline.StepRun:
		texts = append(texts, s.Run)
	case pipeline.StepBuildImage:
		texts = append(texts, s.BuildImage.Image)
		for _, a := range s.BuildImage.BuildArgs {
			texts = append(texts, a.Value)
		}
	case pipeline.StepPushImage:
		texts = append(texts, s.PushImage.Image)
	}
	return distinctRefs(texts...)
}

// TextRefs returns the distinct references in the given template strings, in order.
func TextRefs(texts ...string) []pipeline.Ref { return distinctRefs(texts...) }

func distinctRefs(texts ...string) []pipeline.Ref {
	var out []pipeline.Ref
	seen := map[pipeline.Ref]bool{}
	for _, text := range texts {
		for _, r := range pipeline.MustTemplate(text).Refs() {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	return out
}

// IsCommand reports whether a step renders as a shell command.
func IsCommand(s *pipeline.Step) bool {
	switch s.Kind() {
	case pipeline.StepRun, pipeline.StepBuildImage, pipeline.StepPushImage, pipeline.StepDeployKubernetes:
		return true
	}
	return false
}

// DisplayName returns a step's name, or a generated description. Unnamed run
// steps return "": their script text may hold references or provider macros.
func DisplayName(s *pipeline.Step) string {
	if s.Name != "" {
		return s.Name
	}
	switch s.Kind() {
	case pipeline.StepCheckout:
		return "Checkout"
	case pipeline.StepUses:
		return s.Uses
	case pipeline.StepCache:
		return "Cache " + s.Cache.Key
	case pipeline.StepUploadArtifact:
		return "Upload artifact " + s.UploadArtifact.Name
	case pipeline.StepDownloadArtifact:
		return "Download artifact " + s.DownloadArtifact.Name
	case pipeline.StepPublishTestResults:
		return "Publish test results"
	case pipeline.StepBuildImage:
		return "Build image"
	case pipeline.StepPushImage:
		return "Push image"
	case pipeline.StepDeployKubernetes:
		return "Deploy to Kubernetes"
	}
	return ""
}
