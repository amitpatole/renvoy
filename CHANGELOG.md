# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- The `renvoy.dev/v1alpha1` pipeline format: triggers (push, tags, pull requests,
  manual, schedules), stages and jobs with dependencies, structured conditions,
  runners, containers, matrices, timeouts, retries, environments and approvals,
  variables, secret references, and steps for checkout, scripts, reusable
  provider steps, caches, artifacts, test results, image build and push, and
  Kubernetes deployment.
- Strict parsing with located diagnostics (file, line, column, config path,
  suggestions), reference validation, dependency cycle detection, and artifact
  flow checks.
- Providers: GitHub Actions, GitLab CI, Azure DevOps Pipelines, Jenkins
  Declarative Pipeline, Bamboo YAML Specs, and Bamboo Java Specs, each with a
  capability matrix and golden tests.
- Commands: `init`, `validate` (text and JSON), `generate` (`--dry-run`, `--force`,
  `--all`, `--output`), `diff`, `providers`, `capabilities`, and `version`, with
  stable exit codes.
- Generated-file safety: headers with checksums, preserved custom blocks, refusal
  to overwrite edited files, and symlink-safe atomic writes.
- A JSON Schema for editors, experimental plugin protocol types in `pkg/plugin`,
  examples, and documentation.

### Security

- Bamboo: `${git.branch}` is rejected in task environment values, where a branch
  name containing quotes could alter the parsed environment.
- Warnings when `${git.branch}` is passed to a reusable step input, and when
  values contain provider expression syntax (`${{ }}`, `$[ ]`).
- PowerShell quoting escapes typographic double quotes, which PowerShell treats as
  string delimiters.
- Diagnostics escape terminal control and bidirectional-override characters.
- `renvoy` refuses a non-regular config file (such as a FIFO) before opening it.
- The YAML spacing pass keeps the original text whenever spacing would change the
  parsed document.
- Dependency ancestors are computed once per pipeline and pipelines are limited to
  500 jobs, so a dense stage graph can no longer make validation hang.
- The config path written into generated headers is reduced to inert characters,
  so a directory name cannot add lines (including Java `\u000a` escapes).
- Bamboo env values may only be literals and `${git.sha}`; variables and secrets
  are rejected as well as `${git.branch}`.
- Control variables (`BASH_ENV`, `LD_PRELOAD`, `PATH`, and similar) warn, and are
  rejected when set from `${git.branch}` or used as matrix axes.
- Provider expression warnings cover variables, container settings, step names,
  and Azure `$( )` macros.
- Paths and image names may not start with `-`.
- File names, error messages, and `renvoy diff` output escape terminal control
  characters.
- Output amplification is bounded: variable values (4 KiB), total substituted
  variable bytes (1 MiB), total matrix combinations (1,024), and generated files
  (4 MiB).
- Custom blocks may only add content: YAML must keep every generated section
  identical and stay a single document, Groovy and Java blocks must be balanced,
  and pom.xml blocks must be well-formed. Near-miss markers in any case are refused.
- `${git.branch}` cannot set any variable that selects programs, configuration,
  directories, proxies, or CI runner behavior.
- Warnings for Bamboo `${bamboo.x}` and dotted Azure `$(Name.Value)` substitutions
  in scripts and values; Bamboo env values reject a literal `${`.
- Duplicate keys in `env`, `variables`, `with`, `buildArgs`, and `matrix` are
  errors; GitLab reserves `GIT_STRATEGY`.
- Container and matrix image names are validated after variable substitution.
- Source names in headers collapse `--`, keeping `pom.xml` comments well-formed.
- Custom YAML may not use anchors, aliases, merge keys, or explicit tags, or
  redefine a generated key in any case; GitLab's parser applies a later `<<` merge
  over explicit keys, which let custom content replace a generated job.
- Custom Groovy and Java may not contain `\u` escapes or a bare `/`, which hid
  brackets from the structure check (slashy strings, unicode escapes).
- Branch and tag patterns are limited (255 characters, 100 per list), and the
  condition text repeated on dependent jobs is bounded; identical inherited
  conditions are rendered once.
- More names that select programs, flags, or credential files (`MAKEFLAGS`, `CC`,
  `*_FILE`, `*_ARGS`, `*_WRAPPER`, `TF_*`, `AWS_*`, and others) reject
  `${git.branch}`.
