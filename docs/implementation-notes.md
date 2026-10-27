# Implementation notes

Decisions made while implementing the build specification, including the places
where the implementation differs from or extends it.

## Differences from the specification

| Topic | Specification | Implementation | Why |
| --- | --- | --- | --- |
| YAML library | `gopkg.in/yaml.v3` | `go.yaml.in/yaml/v3` | The `go-yaml/yaml` repository is archived. `go.yaml.in/yaml/v3` is the YAML organization's maintained continuation with the same API and node positions. |
| Release automation | Optional `.goreleaser.yaml` | A documented release plan in [CONTRIBUTING](../CONTRIBUTING.md#releasing) | No release is published yet; a config that cannot be exercised end to end would not be "complete and useful". |
| Providers | Four initial targets | Also `bamboo` (YAML Specs) and `bamboo-java` (Java Specs) | Requested during implementation. Bamboo was already a listed future target. |
| `downloadArtifact.path` | Not specified | Not supported | GitLab and Jenkins cannot restore artifacts to a different directory, so a destination would be lossy. Artifacts restore at their original paths. |

## Design decisions not fixed by the specification

- **Names are kebab-case** (`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`: at most 63 characters, no trailing hyphen). They map
  onto every provider's identifiers (Azure replaces `-` with `_`, Bamboo keys
  upper-case and strip `-`) and are safe in file names, which prevents path
  traversal through `metadata.name`.
- **Job names are unique across the pipeline** and differ from stage names, because
  GitHub, GitLab, and Jenkins share one namespace for them.
- **Stages without `dependsOn` follow the previous stage**, matching GitLab, Azure,
  Jenkins, and Bamboo defaults. `dependsOn: []` starts a stage immediately.
- **Conditions are structured** (`branches`, `events`) rather than free-form
  expressions, so each provider can translate them exactly.
- **Branch patterns are exact names or a trailing-`*` prefix.** Glob semantics
  differ between providers (for example whether `*` crosses `/`); a prefix has one
  meaning everywhere.
- **Variables are literal**, references are `${namespace.name}`, and plain shell
  syntax such as `${HOME}` passes through. `$${` escapes a reference.
- **Secrets are declared by name** so typos are caught and no secret value can
  appear in the file.
- **Builtin image and Kubernetes steps run CLI commands** (`docker`, `kubectl`) on
  every provider, so their behavior is identical and visible in the output.
- **Generated files carry a checksum** of their content outside the custom block.
  That is how Renvoy tells unedited output (safe to regenerate) from hand edits
  (refused without `--force`) without keeping state outside the repository.
- **Generic `uses` references are provider-native.** A non-`checkout` `uses` is a
  GitHub action or an Azure task; providers that cannot run it report an error.
- **GitHub workflows default to `permissions: contents: read`.** Least privilege is
  the safe default; the `permissions` extension grants more.
- **Exit codes** add `5` (diff found differences) and `64` (usage error) to the five
  codes the specification requires.

## Bamboo

- Both Bamboo providers read `extensions.bamboo`. `projectKey` and `planKey` are
  required because YAML Specs cannot create projects.
- The Java Specs project mirrors Atlassian's `bamboo-specs-archetype` 12.1.12. The
  default Specs version is 12.1.12, the newest stable release when this was
  written; it must match the Bamboo server, so `specsVersion` overrides it.
- Generated Java is compiled against the real library and run through Bamboo's
  offline validation in CI (`TestJavaSpecsCompileAndValidate`). That check found
  that Bamboo rejects `" & ' < > \` in plan descriptions, which is now a Renvoy
  error.
- There is no offline validator for YAML Specs; its shapes follow the
  [Bamboo Specs reference](https://docs.atlassian.com/bamboo-specs-docs/latest/specs.html).
