// Package app wires the renvoy command line.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/amitpatole/renvoy/internal/config"
	"github.com/amitpatole/renvoy/internal/diagnostics"
	"github.com/amitpatole/renvoy/internal/pipeline"
	"github.com/amitpatole/renvoy/internal/providers"
	"github.com/amitpatole/renvoy/internal/providers/registry"
	"github.com/amitpatole/renvoy/internal/render"
)

// Exit codes. These are part of the CLI contract.
const (
	ExitOK              = 0
	ExitFailure         = 1
	ExitInvalidSpec     = 2
	ExitIncompatible    = 3
	ExitUnsafeOverwrite = 4
	ExitDiffFound       = 5
	ExitUsage           = 64
)

// Version is set at build time with -ldflags "-X github.com/amitpatole/renvoy/internal/app.Version=v1.2.3".
var Version = ""

// exitError carries an exit code; its message, if any, has already been printed.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

type env struct {
	dir    string
	stdout io.Writer
	stderr io.Writer
}

// Run executes the CLI with args (excluding the program name) and returns the exit code.
func Run(args []string, stdout, stderr io.Writer, dir string) int {
	e := &env{dir: dir, stdout: stdout, stderr: stderr}
	root := e.rootCmd()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(context.Background())
	var ee exitError
	var ue usageError
	switch {
	case err == nil:
		return ExitOK
	case errors.As(err, &ee):
		return ee.code
	case errors.As(err, &ue):
		fmt.Fprintf(stderr, "error: %s\nRun 'renvoy --help' for usage.\n", diagnostics.Sanitize(ue.msg))
		return ExitUsage
	case isCobraUsage(err):
		fmt.Fprintf(stderr, "error: %s\nRun 'renvoy --help' for usage.\n", diagnostics.Sanitize(err.Error()))
		return ExitUsage
	default:
		fmt.Fprintf(stderr, "error: %s\n", diagnostics.Sanitize(err.Error()))
		return ExitFailure
	}
}

func isCobraUsage(err error) bool {
	msg := err.Error()
	for _, s := range []string{"unknown command", "unknown flag", "unknown shorthand flag", "flag needs an argument",
		"invalid argument", "accepts ", "requires at least", "if any flags in the group"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func (e *env) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "renvoy",
		Short:         "Compile one provider-neutral pipeline into native CI/CD configuration",
		Long:          "Renvoy compiles a single renvoy.yaml into native pipeline files for GitHub Actions,\nGitLab CI, Azure DevOps Pipelines, and Jenkins, and reports every portability gap.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(e.initCmd(), e.validateCmd(), e.generateCmd(), e.diffCmd(), e.providersCmd(),
		e.capabilitiesCmd(), e.versionCmd())
	return root
}

func (e *env) abs(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(e.dir, p)
}

// load reads and compiles the config. On failure it prints diagnostics and returns an exitError.
func (e *env) load(file string, format string) (*config.Result, error) {
	path := config.Discover(e.dir, file)
	display := file
	if display == "" {
		display = filepath.Base(path)
	}
	data, err := config.Read(e.abs(path))
	if err != nil {
		d := diagnostics.Diagnostic{Severity: diagnostics.Error, File: display, Message: err.Error()}
		if errors.Is(err, config.ErrNotFound) {
			d.Message = "configuration file not found"
			d.Hint = "run `renvoy init` to create one, or pass --file"
		}
		e.printDiags(diagnostics.List{d}, format)
		return nil, exitError{ExitInvalidSpec}
	}
	res := config.Compile(data, filepath.ToSlash(display), registry.Names())
	return &res, nil
}

func (e *env) printDiags(l diagnostics.List, format string) {
	l = l.Sorted()
	if format == "json" {
		_ = diagnostics.WriteJSON(e.stdout, l)
		return
	}
	if len(l) > 0 {
		_ = diagnostics.WriteText(e.stderr, l)
	}
}

func checkFormat(format string) error {
	if format != "text" && format != "json" {
		return usageError{fmt.Sprintf("unknown format %q (use text or json)", format)}
	}
	return nil
}

func (e *env) validateCmd() *cobra.Command {
	var file, format string
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate renvoy.yaml without selecting a provider",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := checkFormat(format); err != nil {
				return err
			}
			res, err := e.load(file, format)
			if err != nil {
				return err
			}
			e.printDiags(res.Diagnostics, format)
			if res.Diagnostics.HasErrors() {
				return exitError{ExitInvalidSpec}
			}
			if format == "text" {
				fmt.Fprintf(e.stderr, "%s is valid (%d warning(s))\n", diagnostics.Sanitize(res.SourceMap.File), res.Diagnostics.Count(diagnostics.Warning))
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "path to the pipeline file (default renvoy.yaml)")
	cmd.Flags().StringVar(&format, "format", "text", "diagnostic output format: text or json")
	return cmd
}

func selectProviders(names []string, all bool) ([]providers.Provider, error) {
	if all && len(names) > 0 {
		return nil, usageError{"use either --provider or --all, not both"}
	}
	if all {
		return registry.All(), nil
	}
	if len(names) == 0 {
		return nil, usageError{"select a provider with --provider <name> or use --all (see `renvoy providers`)"}
	}
	var out []providers.Provider
	seen := map[string]bool{}
	for _, n := range names {
		p, ok := registry.Get(n)
		if !ok {
			return nil, usageError{fmt.Sprintf("unknown provider %q (available: %s)", n, strings.Join(registry.Names(), ", "))}
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// plan compiles the config, validates each provider, renders, and plans files.
// It prints diagnostics and returns an exitError on any blocking problem.
func (e *env) plan(ctx context.Context, file, output string, provs []providers.Provider, force bool) ([]render.Planned, string, error) {
	res, err := e.load(file, "text")
	if err != nil {
		return nil, "", err
	}
	diags := res.Diagnostics
	if diags.HasErrors() {
		e.printDiags(diags, "text")
		return nil, "", exitError{ExitInvalidSpec}
	}
	for _, p := range provs {
		diags = append(diags, res.SourceMap.Locate(p.Validate(res.Pipeline))...)
	}
	e.printDiags(diags, "text")
	if diags.HasErrors() {
		fmt.Fprintln(e.stderr, "\nno files were written")
		return nil, "", exitError{ExitIncompatible}
	}
	outDir := e.abs(output)
	source := sourceName(e.abs(config.Discover(e.dir, file)), outDir)
	var files []providers.GeneratedFile
	for _, p := range provs {
		f, err := p.Render(ctx, res.Pipeline, providers.RenderOptions{SourceName: source})
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", p.Name(), err)
		}
		files = append(files, f...)
	}
	planned, conflicts, err := render.Plan(outDir, files, source, force)
	if err != nil {
		return nil, "", err
	}
	if len(conflicts) > 0 {
		var l diagnostics.List
		for _, c := range conflicts {
			l = append(l, diagnostics.Diagnostic{Severity: diagnostics.Error, File: c.Path,
				Message: "refusing to overwrite: " + c.Reason, Hint: c.Hint})
		}
		if len(diags) > 0 {
			fmt.Fprintln(e.stderr)
		}
		_ = diagnostics.WriteText(e.stderr, l)
		fmt.Fprintln(e.stderr, "\nno files were written")
		return nil, "", exitError{ExitUnsafeOverwrite}
	}
	return planned, outDir, nil
}

func sourceName(configAbs, outDir string) string {
	if rel, err := filepath.Rel(outDir, configAbs); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.Base(configAbs)
}

func (e *env) generateCmd() *cobra.Command {
	var file, output string
	var names []string
	var all, dryRun, force bool
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate native pipeline files for one or more providers",
		Long: "Generate validates the pipeline for every selected provider before writing anything.\n" +
			"Existing files are only replaced when Renvoy generated them and they were not edited\n" +
			"outside their custom block; use --force to replace other files.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			provs, err := selectProviders(names, all)
			if err != nil {
				return err
			}
			planned, outDir, err := e.plan(cmd.Context(), file, output, provs, force)
			if err != nil {
				return err
			}
			if dryRun {
				for _, p := range planned {
					if len(planned) > 1 {
						fmt.Fprintf(e.stdout, "==> %s <==\n", p.Path)
					}
					_, _ = e.stdout.Write(p.Content)
				}
				return nil
			}
			if err := render.Write(outDir, planned); err != nil {
				return err
			}
			for _, p := range planned {
				fmt.Fprintf(e.stderr, "%-9s %s\n", p.Action, p.Path)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "path to the pipeline file (default renvoy.yaml)")
	cmd.Flags().StringSliceVarP(&names, "provider", "p", nil, "provider to generate (repeatable)")
	cmd.Flags().BoolVar(&all, "all", false, "generate every provider")
	cmd.Flags().StringVarP(&output, "output", "o", ".", "directory to write files under")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print generated files to stdout instead of writing them")
	cmd.Flags().BoolVar(&force, "force", false, "replace files that were edited or not generated by Renvoy (custom blocks are kept)")
	return cmd
}

func (e *env) diffCmd() *cobra.Command {
	var file, output string
	var names []string
	var all bool
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Show how generated files differ from what is on disk",
		Long:  "Diff prints a unified diff per changed file. It exits 0 when files are up to date and 5 when they differ.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			provs, err := selectProviders(names, all)
			if err != nil {
				return err
			}
			planned, _, err := e.plan(cmd.Context(), file, output, provs, true)
			if err != nil {
				return err
			}
			changed := false
			for _, p := range planned {
				if p.Action == render.Unchanged {
					continue
				}
				changed = true
				from := "a/" + p.Path
				if p.Existing == nil {
					from = "/dev/null"
				}
				// Existing files may come from an untrusted checkout; never send
				// their control characters to the terminal raw.
				fmt.Fprint(e.stdout, diagnostics.SanitizeText(render.UnifiedDiff(from, "b/"+p.Path, p.Existing, p.Content)))
			}
			for _, p := range planned {
				if n := render.CustomLines(p); n > 0 {
					fmt.Fprintf(e.stderr, "note: %s has %d line(s) of custom content, which renvoy.yaml does not describe\n", p.Path, n)
				}
			}
			if changed {
				return exitError{ExitDiffFound}
			}
			fmt.Fprintln(e.stderr, "generated files are up to date")
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "path to the pipeline file (default renvoy.yaml)")
	cmd.Flags().StringSliceVarP(&names, "provider", "p", nil, "provider to compare (repeatable)")
	cmd.Flags().BoolVar(&all, "all", false, "compare every provider")
	cmd.Flags().StringVarP(&output, "output", "o", ".", "directory the files live under")
	return cmd
}

func (e *env) initCmd() *cobra.Command {
	var file string
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a commented starter renvoy.yaml",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if file == "" {
				file = config.DefaultFile
			}
			path := e.abs(file)
			if info, err := os.Lstat(path); err == nil {
				if !force {
					fmt.Fprintf(e.stderr, "error: %s already exists\n  hint: pass --force to replace it\n", diagnostics.Sanitize(file))
					return exitError{ExitUnsafeOverwrite}
				}
				if !info.Mode().IsRegular() {
					fmt.Fprintf(e.stderr, "error: %s is not a regular file; refusing to replace it\n", diagnostics.Sanitize(file))
					return exitError{ExitUnsafeOverwrite}
				}
			}
			if err := os.WriteFile(path, []byte(Starter), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(e.stderr, "created %s\nnext: renvoy validate && renvoy generate --all --dry-run\n", diagnostics.Sanitize(file))
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "path to write (default renvoy.yaml)")
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing file")
	return cmd
}

func (e *env) providersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "providers",
		Short: "List compiled-in providers",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
			for _, p := range registry.All() {
				fmt.Fprintf(tw, "%s\t%s\n", p.Name(), p.Description())
			}
			return tw.Flush()
		},
	}
}

func (e *env) capabilitiesCmd() *cobra.Command {
	var name, format string
	cmd := &cobra.Command{
		Use:   "capabilities",
		Short: "Show how a provider supports each portable concept",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := checkFormat(format); err != nil {
				return err
			}
			if name == "" {
				return usageError{"select a provider with --provider <name>"}
			}
			p, ok := registry.Get(name)
			if !ok {
				return usageError{fmt.Sprintf("unknown provider %q (available: %s)", name, strings.Join(registry.Names(), ", "))}
			}
			caps := p.Capabilities().Ordered()
			if format == "json" {
				enc := json.NewEncoder(e.stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(struct {
					Provider     string                     `json:"provider"`
					Capabilities []providers.CapabilityInfo `json:"capabilities"`
				}{p.Name(), caps})
			}
			fmt.Fprintf(e.stdout, "%s (%s)\n\n", p.DisplayName(), p.Name())
			tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "CAPABILITY\tSUPPORT\tNOTE")
			for _, c := range caps {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", c.ID, c.Support, c.Note)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVarP(&name, "provider", "p", "", "provider to describe")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return cmd
}

func (e *env) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the Renvoy version",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Fprintf(e.stdout, "renvoy %s (%s, %s/%s, config %s)\n", version(), runtime.Version(),
				runtime.GOOS, runtime.GOARCH, pipeline.APIVersionV1Alpha1)
			return nil
		},
	}
}

func version() string {
	if Version != "" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
