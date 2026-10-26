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
  and pom.xml blocks must be well-formed.
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
- Custom blocks may not add top-level keys that apply to every generated job
  (GitHub `env`/`defaults`, GitLab `default`/`include`, Azure `pool`/`extends`,
  Bamboo `docker`/`branch-overrides`, and others), nor YAML 1.1 boolean or null
  keys.
- Custom content may not contain carriage returns or NEL/LS/PS line separators
  (javac ends a `//` comment at a lone CR, which hid a closing brace), Jenkinsfile
  custom code may not use `${...}` interpolation, and pom.xml custom content may
  not contain DOCTYPE, ENTITY, or processing instructions.
- `renvoy diff` notes files whose custom block holds content.
- `${git.branch}` may only set variables with neutral names (containing `BRANCH`,
  `REF`, `TAG`, `VERSION`, `NAME`, or `LABEL`) instead of relying on a denylist.
- Matrices are limited to 16 axes and 1 MiB of entries across combinations (Azure
  writes every axis into every combination), and names to 128 characters.
- Custom content rejects invisible format characters, float-like and
  trailing-space keys, pom.xml declarations other than comments and CDATA, and
  pipeline-wide Jenkinsfile directives.
- Matrix budget checks no longer loop per combination, so an oversized matrix with
  a container image fails immediately instead of hanging validation.
- Build arguments follow the same `${git.branch}` rules as environment variables
  (Docker applies `HTTP_PROXY` and similar to every `RUN`).
- Jenkinsfile custom blocks are checked against an allowlist (only `post`
  sections at the top level), closing quoted-name and line-continuation bypasses;
  `${...}` is refused only inside double-quoted strings.
- Custom content must be valid UTF-8 and YAML custom content may not contain tabs;
  the generated pom.xml sets `project.build.sourceEncoding` to UTF-8.
- The `${git.branch}` allowlist matches whole words and adds `ENV`, `ENVIRONMENT`,
  `CHANNEL`, `TARGET`, `URL`, and `SLUG`; step names or scripts that mention a
  marker no longer block generation; a custom GitLab `pages` job is allowed.
- Cron expressions are limited to 256 characters and validated in linear time
  (a 960 KB cron took 19 s); YAML custom blocks are limited to 256 KiB and 10,000
  nodes and checked before the quadratic map decode (100k keys took over 60 s).
- Values may not contain custom-block marker text, so no generated line can look
  like a custom block; `renvoy diff` counts custom lines reliably.
- The `${git.branch}` rules cover more tool settings (`VIRTUAL_ENV`, `PYENV_*`,
  `*_FILES`, `*_DIRS`, `*_HOST`, `SONAR_*`, `COMPOSE_*`, and others).
- Jenkinsfile custom blocks treat strings at the top level as code (`'agent'
  'any'` is refused) and allow escaped `\${...}`; pom.xml custom content must use
  unprefixed names, unique attributes, and legal XML characters.
- The marker-text check on `renvoy.yaml` covers every string field.
- pom.xml custom content may not use `:` in names or character references to
  illegal XML characters; cron items are parsed strictly (no zero steps, reversed
  ranges, or malformed items); Java custom code may use division.
- `--force` saves the file it replaces as `<file>.renvoy-backup` (numbered when one
  exists, never through a symlink), so it cannot lose content. This replaces the
  misspelled-marker heuristic, which could not be both safe and free of false
  positives: legitimate files that mention "renvoy end" now regenerate normally.
- pom.xml custom checks use the token stream for names and skip comments and CDATA
  for character references, so colons and references in comments are accepted;
  cron steps may equal the field width.
- Backup paths are resolved and checked while planning, so an unusable backup path
  refuses the whole run before any file is written; backups keep the replaced
  file's permissions; files over the 4 MiB read limit get a hint that fits.
- `metadata.labels` is limited to 100 entries and rejects duplicate keys, and
  every plain mapping (such as provider extension settings) is limited to 256
  entries and checked for duplicates before the YAML library decodes it, whose
  duplicate check is quadratic (70k labels took 38 s; now 0.2 s).
- Updating an unedited generated file keeps its permissions.
- Schedules are limited to 20 with at most 100 branches each, and the Jenkins
  cron expression is built in linear time (40k branches took 43 s); `env`, `with`,
  `variables`, and build arguments are limited to 1,000 entries, since providers
  render them as mappings that the YAML library checks in quadratic time (49k
  entries took 38 s). Both reproductions now fail in about 0.1 s.
- When a write fails partway through a multi-file `generate`, the files already
  written are listed, followed by a "stopped" line.
- Jenkins rejects variable, job `env`, and secret names that Groovy cannot assign
  in an `environment` block (keywords such as `if`, and default-imported class
  names such as `URL` or `UUID`), instead of writing a Jenkinsfile that fails to
  compile. The 584 class names were listed from JDK 21 and Groovy 4 and each was
  confirmed with the Groovy compiler.
