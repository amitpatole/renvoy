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
secrets: [REGISTRY_TOKEN]
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
[`schemas/renvoy.v1alpha1.schema.json`](../schemas/renvoy.v1alpha1.schema.json).
`renvoy validate` is the authoritative check: it also enforces references, the
dependency graph, and naming rules that a schema cannot express.

## Conventions

- **Names.** Pipeline, stage, and job names use lowercase letters, digits, and
  hyphens, start with a letter, and are at most 63 characters. Job names are
  unique across the whole pipeline and must differ from stage names. These rules
  keep names valid as identifiers and file names on every provider.
- **Variable names.** Variables, secrets, environment variables, and matrix axes
  use letters, digits, and underscores, start with a letter or underscore, and
  must not start with `RENVOY_` (reserved for generated bindings). Variable and
  secret names must be unique ignoring case, because Azure DevOps upper-cases them.
- **Control variables.** Environment variables that change how shells, runtimes,
  or git behave (`BASH_ENV`, `ENV`, `PATH`, `LD_PRELOAD`, `NODE_OPTIONS`,
  `PYTHONSTARTUP`, `JAVA_TOOL_OPTIONS`, `GIT_SSH_COMMAND`, `GIT_CONFIG_*`, and
  similar) produce a warning and cannot be matrix axes. `${git.branch}` cannot set
  any variable that selects programs, configuration, directories, proxies, or CI
  runner behavior (for example `GIT_*`, `CI_*`, `KUBECONFIG`, `HOME`, `XDG_*`,
  `DOCKER_*`, `*_OPTS`, `*PATH`, `*_HOME`); use a neutral name such as `BRANCH_NAME`.
- **Duplicate keys** in `variables`, `env`, `with`, `buildArgs`, and `matrix` are errors.
- **Paths.** Paths are relative to the workspace, use `/`, never contain `..`, and
  never start with `-`.
  Artifact and cache paths are literal (no wildcards); directories end with `/`.
- **Unknown fields are errors**, with the line, column, and a suggestion.

## Top level

| Field | Required | Description |
| --- | --- | --- |
| `apiVersion` | yes | `renvoy.dev/v1alpha1` |
| `kind` | yes | `Pipeline` |
| `metadata` | yes | `name` (required), `description`, `labels` (string map) |
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

- A push trigger with neither `branches` nor `tags` fires for every branch and tag.
  With only `branches`, tags do not fire it; with only `tags`, branches do not.
- **Patterns** are exact names or a prefix ending in `*`. `release/*` matches every
  name starting with `release/`, including nested names such as `release/1.2/rc`.
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
| `run` | `run`, `shell` (`bash`, `sh`, `pwsh`), `workingDirectory` | Default shell: `bash` on Linux and macOS, `pwsh` on Windows. Bash stops at the first failing command (`-eo pipefail`). |
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

Custom content may only add to a file: new top-level keys in YAML, balanced code
in a Jenkinsfile or Java Specs, and well-formed elements in `pom.xml`. Content that
would change the generated part (for example an indented line that joins a
generated job), an extra YAML document, or a near-miss marker is refused.

`renvoy generate` replaces a file only when it is new, or when Renvoy generated it
and nobody edited it outside the custom block (the checksum still matches).
Otherwise it stops with exit code 4 and writes nothing; review with `renvoy diff`
and pass `--force` to replace the file. Custom blocks are kept even with
`--force`, and a malformed custom block is never overwritten.

## Command line

| Command | Description |
| --- | --- |
| `renvoy init [--file F] [--force]` | Write a commented starter `renvoy.yaml`. |
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
nodes reached through aliases), pipelines up to 500 jobs, variable values up to
4 KiB, 1 MiB of variable values substituted at references, 1,024 matrix
combinations in total, and generated or existing files up to 4 MiB.
