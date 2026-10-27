# renvoy.yaml reference (`renvoy.dev/v1alpha1`)

A `renvoy.yaml` file describes one pipeline in provider-neutral terms. Renvoy
validates it, then compiles it into native files for each provider you select.

```yaml
apiVersion: renvoy.dev/v1alpha1
kind: Pipeline
metadata:
  name: web-service
triggers:
  push:
    branches: [main]
  pullRequest: {}
variables:
  IMAGE: ghcr.io/acme/web-service
secrets: [REGISTRY_PASSWORD]
stages:
  - name: test
    jobs:
      - name: unit-tests
        container:
          image: python:3.12
        steps:
          - uses: checkout
          - run: pytest --junitxml=reports/junit.xml
          - publishTestResults:
              path: reports/junit.xml
```

Editors can use the JSON Schema in
[`schemas/renvoy.v1alpha1.schema.json`](https://github.com/amitpatole/renvoy/blob/main/schemas/renvoy.v1alpha1.schema.json).
`renvoy validate` is the authoritative check: it also enforces references, the
dependency graph, and naming rules that a schema cannot express.

## Conventions

- **Names.** Pipeline, stage, and job names use lowercase letters, digits, and
  hyphens, start with a letter, and are at most 63 characters. Job names are
  unique across the whole pipeline and must differ from stage names. These rules
  keep names valid as identifiers and file names on every provider; for the same
  reason a pipeline cannot be named after a Windows device (`con`, `prn`, `aux`,
  `nul`, `com0`-`com9`, `lpt0`-`lpt9`).
- **Variable names.** Variables, secrets, environment variables, and matrix axes
  use letters, digits, and underscores, start with a letter or underscore, and
  must not start with `RENVOY_` (reserved for generated bindings). Variable and
  secret names must be unique ignoring case, because Azure DevOps upper-cases them.
  For the same reason, and because Windows environment names ignore case, the
  entries of one `env` map and the axes of one matrix must differ by more than case.
- **Image names** follow the Docker reference grammar once variables are
  substituted: an optional registry host, lowercase path components, an optional
  `:tag`, and an optional digest (`@sha256:` with 64 lowercase hex digits, or
  sha384 or sha512), at most 255 characters. IPv6 registry hosts use brackets.
- **Control variables.** Environment variables that change how shells, runtimes,
  or git behave (`BASH_ENV`, `ENV`, `PATH`, `LD_PRELOAD`, `NODE_OPTIONS`,
  `PYTHONSTARTUP`, `JAVA_TOOL_OPTIONS`, `GIT_SSH_COMMAND`, `GIT_CONFIG_*`, and
  similar) produce a warning and cannot be matrix axes.
- **Branch names in variables.** `${git.branch}` may only set environment variables
  and build arguments whose names describe the value, meaning they contain one of
  the words `BRANCH`, `REF`, `TAG`, `VERSION`, `NAME`, `LABEL`, `ENV`, `ENVIRONMENT`,
  `CHANNEL`, `TARGET`, `URL`, or `SLUG` (for example `RELEASE_BRANCH`, `IMAGE_TAG`,
  `DEPLOY_ENV`), and do not select programs, configuration, directories, hosts,
  proxies, tool versions, or CI runner behavior (`GIT_*`, `CI_*`, `BUILD_*`,
  `PYENV_*`, `VIRTUAL_ENV`, `*_FILE(S)`, `*_DIR(S)`, `*_HOST`, `*_OPTS`, `*PATH`,
  `HTTP_PROXY`, and similar).
- **Duplicate keys** in `variables`, `env`, `with`, `buildArgs`, and `matrix` are errors.
- **Paths.** Paths are relative to the workspace, use `/`, never contain `..`, and
  never start with `-`.
  Artifact and cache paths are literal (no wildcards); directories end with `/`.
  Paths must not start with `!` or `#`, contain `\`, or start or end with spaces,
  which GitHub's artifact and cache actions read as exclusions, comments, or escapes.
  Artifact, cache, and test-result paths also have no `.` or empty segments
  (`dist/`, not `./dist/`), because Jenkins' Ant patterns would match no files.
  No path contains control or invisible format characters, or a segment ending
  in `.` or a space, which Windows removes.
- **Unknown fields are errors**, with the line, column, and a suggestion.

## Top level

| Field | Required | Description |
| --- | --- | --- |
| `apiVersion` | yes | `renvoy.dev/v1alpha1` |
| `kind` | yes | `Pipeline` |
| `metadata` | yes | `name` (required), `description`, `labels` (string map, at most 100) |
| `triggers` | yes | At least one trigger. |
| `variables` | no | Plain, non-secret values shared by every job. Values are literal. |
| `secrets` | no | Names of secrets stored in the CI system. Never values. |
| `stages` | yes | At least one stage. |
| `extensions` | no | Provider-specific settings; see [providers](providers.md#provider-extensions). |

## Triggers

```yaml
triggers:
  push:
    branches: [main, release/*]   # omit for every branch
    tags: [v*]
  pullRequest:
    branches: [main]              # target branches; omit for all
  manual: {}
  schedules:
    - cron: "0 3 * * 1"           # five fields, UTC
      branches: [main]            # exact branch names
```

Cron items are `*`, a number, or a range `a-b`, each optionally followed by
`/step`, separated by commas; values must be within the field's range and ranges
must not be reversed.

- A push trigger with neither `branches` nor `tags` fires for every branch and tag.
  With only `branches`, tags do not fire it; with only `tags`, branches do not.
- **Patterns** are exact names or a prefix ending in `*`, using letters, digits,
  `.`, `_`, `/`, and `-`. `release/*` matches every name starting with
  `release/`, including nested names such as `release/1.2/rc`. Branch conditions
  match branch builds only, never tag or pull request builds.
- **Cron** expressions separate their five fields with single spaces, with no
  leading or trailing space; tabs, newlines, and Unicode spaces are an error.
- `{}` enables a trigger. An empty value (`pullRequest:`) is an error, so a
  forgotten value is never silently ignored.

## Stages and jobs

```yaml
stages:
  - name: build
    dependsOn: [test]          # omit to depend on the previous stage; [] to start at once
    when: {branches: [main]}   # applies to every job in the stage
    jobs:
      - name: image
        dependsOn: [compile]   # jobs in the same stage
        runsOn: linux          # linux (default), windows, macos
        container:
          image: python:${matrix.python}
          options: --cpus 2
        environment: staging   # or {name, url, approvals: {required: true}}
        env:
          LOG_LEVEL: debug
        when:
          branches: [main]
          events: [push, manual]   # push, pullRequest, manual, schedule
        timeoutMinutes: 30
        retries: 1
        matrix:
          python: ["3.11", "3.12"]
        steps: [...]
```

- Stages form a dependency graph. A stage without `dependsOn` depends on the stage
  declared before it, matching how GitLab, Azure, Jenkins, and Bamboo order stages.
  Cycles and unknown names are errors, reported with the cycle path.
- Jobs in a stage run in parallel unless they depend on each other.
- **Conditions.** `when` holds `branches` and `events`; both must match when both are
  present. Branch conditions match branch builds only, never pull request builds.
  A job whose condition is false is skipped, and so is everything that depends on it.
  Empty `branches` or `events` lists are an error.
  An event with no matching trigger (`manual` without `triggers.manual`) is a
  warning, and so is a condition listing `branches` with only the `pullRequest`
  event, since neither can ever match.
- **Secrets on pull requests.** Providers withhold secrets from pull request
  builds of forks by default (GitHub passes empty values; Azure does not expose
  secret variables to fork builds; GitLab runs fork pipelines in the fork's
  project). Do not rely on secrets in jobs that run for `pullRequest`, and check
  each provider's fork settings before enabling them.
- **Artifact retention** is not set by Renvoy; each provider's default applies.
- **Containers** require a Linux runner.
- **Matrix** runs the job once per combination (at most 256). Values are referenced as
  `${matrix.<axis>}`. Matrix jobs cannot upload artifacts in `v1alpha1`.
- **Environments** name a deployment target. `approvals.required: true` asks for a
  human approval before the job runs; see [providers](providers.md#differences-renvoy-reports).

## Steps

Every step does exactly one thing. All steps accept `name`, `env`, and `when`;
`run` steps also accept `shell` and `workingDirectory`.

| Step | Fields | Notes |
| --- | --- | --- |
| `uses: checkout` | | Checks out the repository. |
| `run` | `run`, `shell` (`bash`, `sh`, `pwsh`), `workingDirectory` | Default shell: `bash` on Linux and macOS, `pwsh` on Windows. Bash stops at the first failing command (`-eo pipefail`); PowerShell 7.4+ does too (`$PSNativeCommandUseErrorActionPreference`). |
| `uses: <ref>` | `uses`, `with` | A provider-native reusable step: a GitHub action (`owner/repo@ref`) or an Azure task (`Name@1`). Other providers report an error. |
| `cache` | `key`, `files`, `paths` | Restores before later steps and saves when the job succeeds. `files` are hashed into the key. |
| `uploadArtifact` | `name`, `paths` | Names are unique per pipeline. |
| `downloadArtifact` | `name` | Restores files at their original paths. The producer must be upstream. |
| `publishTestResults` | `path`, `format` (`junit`) | Runs even when earlier steps fail. `path` may contain wildcards. |
| `buildImage` | `image`, `context`, `dockerfile`, `buildArgs` | Runs `docker build`. |
| `pushImage` | `image` | Runs `docker push`. The runner must already be logged in. |
| `deployKubernetes` | `manifests`, `namespace` | Runs `kubectl apply --recursive`. |

## References

Strings in `run`, `env`, `with`, image names, and build arguments can reference values:

| Reference | Value |
| --- | --- |
| `${variables.NAME}` | A top-level variable. |
| `${secrets.NAME}` | A declared secret. Not allowed in image names or build arguments. |
| `${git.sha}` | The commit being built. |
| `${git.branch}` | The branch name (the source branch for pull requests). |
| `${matrix.AXIS}` | The current matrix value. |

In scripts, references become environment variable expansions (`${NAME}` in bash
and sh, `${env:NAME}` in PowerShell), so they follow shell quoting rules: a
reference inside single quotes is not expanded. Other `$` text, such as `$HOME`
or `${HOME}`, passes through unchanged. Write `$${` for a literal `${`.

| Field | Allowed namespaces |
| --- | --- |
| `run`, `env`, `with` | variables, secrets, git, matrix |
| `buildImage.image`, `pushImage.image`, `buildArgs` | variables, git, matrix |
| `container.image` | variables, matrix |
| everything else | none (literal) |

`${git.branch}` comes from whoever creates the branch. Renvoy only passes it to
scripts through environment variables, warns when it is given to a reusable step
input (`with`), and refuses it in control variables such as `BASH_ENV`. Provider
expression syntax (`${{ }}`, `$[ ]`, `$( )`, `${bamboo.x}`) in variables, env,
`with`, container settings, and step names produces a warning because some
providers evaluate it anywhere in the generated file. In run scripts, `${{ }}`,
`${bamboo.x}`, and dotted Azure macros such as `$(Build.SourceBranchName)` warn,
because the provider pastes their values into the script text; plain shell
`$(command)` does not.

## Generated files

Generated files start with a header and a checksum:

```yaml
# Code generated by Renvoy; DO NOT EDIT.
# Source: renvoy.yaml
# Checksum: sha256:...
```

Each file has a custom block that survives regeneration:

```yaml
# renvoy:begin custom
# Provider-specific content retained during regeneration.
# renvoy:end custom
```

Custom content may only add to a file:

- **YAML:** new top-level keys only (compared ignoring case), as one document,
  without anchors, aliases, merge keys (`<<`), or explicit tags. Different YAML
  parsers resolve those differently, so they could change a generated job in the
  provider's eyes while looking unchanged to Renvoy. Keys must be plain names, not
  YAML 1.1 booleans or nulls (`yes`, `on`, `null`). Keys that apply to every
  generated job are refused: GitHub custom blocks may only add `concurrency` and
  `run-name`; Azure only `name`, `lockBehavior`, and `appendCommitMessageToRunName`;
  GitLab refuses global keywords such as `default`, `include`, `variables`, and
  `image` (new jobs are fine); Bamboo refuses plan-wide keys such as `docker`,
  `variables`, `triggers`, `branches`, and `branch-overrides`.
- **Jenkinsfile and Java Specs:** balanced code without `\u` escapes. Jenkinsfile
  code also may not use a bare `/` outside strings and comments (division,
  regular expression literals, and Groovy slashy strings), whose extent the
  check cannot determine; Java has no slashy strings, so division is fine there. Jenkinsfile
  custom code may only add `post { ... }` sections at its top level (pipeline-wide
  directives such as `options`, `environment`, `tools`, and `agent` would apply to
  every generated stage) and may not use `${...}` inside double-quoted strings
  (single-quoted strings such as `sh 'echo ${VAR}'` and escaped `\${VAR}` are fine). The Java Specs
  `customize(Plan)` method is intentionally free to change the plan.
- **pom.xml:** well-formed elements and attributes without `:` in their names,
  unique attributes, comments, and CDATA only, using legal XML characters
  (including in character references); no DOCTYPE, ENTITY, ATTLIST, other
  declarations, or processing instructions.
- **Every format:** valid UTF-8 without carriage returns, Unicode line separators
  (NEL, LS, PS), or invisible format characters (BOM, zero-width, bidirectional
  controls), and no tabs in YAML, all of which parsers read differently. YAML
  custom blocks are limited to 256 KiB and 10,000 nodes. Values in `renvoy.yaml`
  may not contain marker text (`renvoy:begin custom` / `renvoy:end custom`).

`renvoy diff` notes each file whose custom block holds content, because that
content is preserved but not described by `renvoy.yaml`.

Content that would change the generated part is refused. Only the exact marker
lines delimit a custom block; a misspelled marker is ordinary edited content.

`renvoy generate` replaces a file only when it is new, or when Renvoy generated it
and nobody edited it outside the custom block (the checksum still matches).
Otherwise it stops with exit code 4 and writes nothing; review with `renvoy diff`
and pass `--force` to replace the file. With `--force`, Renvoy first saves the
current file next to it as `<file>.renvoy-backup` (then `.renvoy-backup.1`, and so
on), so nothing is lost, including content between misspelled markers. Providers
ignore these backups; delete them or add `*.renvoy-backup*` to `.gitignore`.
Custom blocks are kept with `--force`, and a structurally malformed custom block
(two openings, no closing) is never overwritten.

## Command line

| Command | Description |
| --- | --- |
| `renvoy init [--file F] [--force]` | Write a commented starter `renvoy.yaml`; `--force` saves an existing file as `.renvoy-backup` first. |
| `renvoy validate [--file F] [--format text\|json]` | Validate without choosing a provider. |
| `renvoy generate --provider P [--provider P2] [--output DIR] [--dry-run] [--force]` | Generate files. |
| `renvoy generate --all [...]` | Generate for every provider; all are validated before anything is written. |
| `renvoy diff --provider P \| --all [--output DIR]` | Show how files on disk differ from what would be generated. |
| `renvoy providers` | List providers. |
| `renvoy capabilities --provider P [--format text\|json]` | Show a provider's capability matrix. |
| `renvoy version` | Print the version. |

Diagnostics go to stderr; `--dry-run` file content and JSON reports go to stdout.

| Exit code | Meaning |
| --- | --- |
| 0 | Success. |
| 1 | Unexpected runtime failure. |
| 2 | Invalid specification (including a missing file). |
| 3 | A selected provider cannot express the pipeline. |
| 4 | Unsafe overwrite refused. |
| 5 | `renvoy diff` found differences. |
| 64 | Command-line usage error. |

## Limits

Config files are read up to 1 MiB, YAML documents up to 100,000 nodes (including
nodes reached through aliases), 1,000 entries in `variables`, `env`, `with`, and
build arguments, 256 entries in other mappings (100 labels), 20 schedules,
pipelines up to 500 jobs, variable values up to
4 KiB, 1 MiB of variable values substituted at references, 1,024 matrix
combinations in total, branch and tag patterns up to 255 characters and 100 per
list, cron expressions up to 256 characters, 1 MiB of condition text repeated on
dependent jobs, 16 matrix axes per job,
1 MiB of matrix entries (axis text times combinations), names up to 128
characters, matrix values up to 64 characters, `timeoutMinutes` up to 525,600
(one year), and generated or existing files up to 4 MiB.
