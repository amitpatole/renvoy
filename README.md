# Renvoy

Renvoy compiles one provider-neutral CI/CD definition into native pipeline files
for GitHub Actions, GitLab CI, Azure DevOps Pipelines, Jenkins, and Atlassian
Bamboo (YAML and Java Specs).

You keep a single `renvoy.yaml`. Renvoy validates it, tells you exactly what a
provider cannot express, and writes readable files that look like a person wrote
them for that provider. It never silently drops behavior, and it never
overwrites files you edited by hand.

- **Local-first.** One binary. No account, no service, no telemetry, no network.
- **Native output.** `needs` on GitHub and GitLab, deployment jobs on Azure,
  `post { always { junit } }` on Jenkins, manual stages on Bamboo.
- **No lossy conversion.** Every provider declares a capability matrix; anything it
  cannot express is an error with the file, line, and a fix.
- **Safe regeneration.** Generated files carry a checksum and a custom block that
  survives regeneration. Edited files are only replaced with `--force`.

> Status: pre-release (`renvoy.dev/v1alpha1`). The configuration format may change
> before `v1`.

## Install

Download a prebuilt binary for Linux, macOS, or Windows (amd64 and arm64) from
the [latest release](https://github.com/amitpatole/renvoy/releases/latest).
Each release has SHA-256 checksums in `checksums.txt`, a Sigstore signature over
them, and GitHub build provenance; [SECURITY.md](SECURITY.md#verifying-a-release)
shows how to verify a download.

With Go 1.27 or newer:

```bash
go install github.com/amitpatole/renvoy/cmd/renvoy@v0.1.0
```

or build it from source:

```bash
git clone https://github.com/amitpatole/renvoy && cd renvoy
make build                 # writes bin/renvoy
```

## Five-minute quick start

Create a starter pipeline and check it:

```console
$ renvoy init
created renvoy.yaml
next: renvoy validate && renvoy generate --all --dry-run
$ renvoy validate
renvoy.yaml is valid (0 warning(s))
```

Generate files for the providers you use. Renvoy prints what each provider
handles differently, then writes the files:

```console
$ renvoy generate --provider github-actions --provider gitlab --provider jenkins
info [github-actions] renvoy.yaml:
  the workflow token is limited to contents: read
  hint: grant more with extensions.github-actions.permissions, e.g. packages: write to push images

info [jenkins] renvoy.yaml:23:1 secrets:
  create Jenkins secret text credentials with the IDs REGISTRY_PASSWORD

warning [jenkins] renvoy.yaml:62:9 stages[2].jobs[0].environment:
  Jenkins has no deployment environments; "staging" is only exported as RENVOY_ENVIRONMENT
create    .github/workflows/my-service.yml
create    .gitlab-ci.yml
create    Jenkinsfile
```

(Some info lines are omitted above.) Use `--dry-run` to print files instead of
writing them, and `--all` for every provider.

When a provider cannot express something, nothing is written:

```console
$ renvoy generate --provider azure-devops      # after adding "retries: 1" to a job
error [azure-devops] renvoy.yaml:31:9 stages[0].jobs[0].retries:
  Azure DevOps Pipelines cannot express jobs.retries
  hint: Azure Pipelines cannot retry jobs; retry inside the step script or remove retries

no files were written
```

Hand edits are protected. Here the first `runs-on` line of the generated
workflow was edited to use `ubuntu-24.04`:

```console
$ renvoy generate -p github-actions
error .github/workflows/my-service.yml:
  refusing to overwrite: the file was edited outside its custom block
  hint: review with `renvoy diff`, move hand-written content into a custom block, or pass --force to replace it (the current file is saved as a .renvoy-backup)

no files were written
$ renvoy diff -p github-actions
--- a/.github/workflows/my-service.yml
+++ b/.github/workflows/my-service.yml
@@ -21,7 +21,7 @@

 jobs:
   unit-tests:
-    runs-on: ubuntu-24.04
+    runs-on: ubuntu-latest
     container:
       image: python:3.12
     steps:
```

`renvoy generate --force` replaces the file anyway and saves the edited version as
`.github/workflows/my-service.yml.renvoy-backup` first.

Put provider-specific additions inside the file's custom block, or set them in
`renvoy.yaml` with a [provider extension](docs/providers.md#provider-extensions)
(here, `extensions.github-actions.runners.linux: [ubuntu-24.04]`).

## A pipeline

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

stages:
  - name: test
    jobs:
      - name: unit-tests
        container:
          image: python:3.12
        steps:
          - uses: checkout
          - run: pip install -r requirements.txt
          - run: pytest --junitxml=reports/junit.xml
          - publishTestResults:
              path: reports/junit.xml

  - name: build
    jobs:
      - name: image
        steps:
          - uses: checkout
          - buildImage:
              context: .
              image: ${variables.IMAGE}:${git.sha}
          - pushImage:
              image: ${variables.IMAGE}:${git.sha}
```

More in [`examples/`](examples) and the [configuration reference](docs/specification.md).

## Providers

| Provider | `--provider` | Output |
| --- | --- | --- |
| GitHub Actions | `github-actions` | `.github/workflows/<name>.yml` |
| GitLab CI | `gitlab` | `.gitlab-ci.yml` |
| Azure DevOps Pipelines | `azure-devops` | `azure-pipelines.yml` |
| Jenkins Declarative Pipeline | `jenkins` | `Jenkinsfile` |
| Bamboo YAML Specs | `bamboo` | `bamboo-specs/bamboo.yaml` |
| Bamboo Java Specs | `bamboo-java` | `bamboo-specs/` Maven project |

`renvoy capabilities --provider <name>` shows what each one supports. The full
matrix and the semantic differences Renvoy handles are in
[docs/providers.md](docs/providers.md).

## Commands

```text
renvoy init                        write a commented starter renvoy.yaml
renvoy validate [--format json]    validate without choosing a provider
renvoy generate -p <provider>      generate files (--all, --dry-run, --force, --output)
renvoy diff -p <provider>          show differences from the files on disk (exit 5 if any)
renvoy providers                   list providers
renvoy capabilities -p <provider>  show a capability matrix (--format json)
renvoy version                     print the version
```

## Documentation

- [Configuration reference](docs/specification.md)
- [Providers and capability matrix](docs/providers.md)
- [Architecture](docs/architecture.md) and [ADR 0001](docs/adr/0001-provider-neutral-ir.md)
- [External provider plugins (proposed)](docs/plugins.md)
- [Implementation notes](docs/implementation-notes.md)
- [Contributing](CONTRIBUTING.md) and [security policy](SECURITY.md)

## License

[Apache-2.0](LICENSE).
