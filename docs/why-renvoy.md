# Why Renvoy

Renvoy compiles one provider-neutral CI/CD definition, `renvoy.yaml`, into native
pipeline files for GitHub Actions, GitLab CI, Azure DevOps Pipelines, Jenkins
Declarative Pipeline, and Atlassian Bamboo (YAML Specs and Java Specs). This page
explains the problem it addresses, what it does differently, who gains what from
adopting it, and when it is the wrong tool.

## The problem

A CI pipeline is usually written in one vendor's dialect. That is fine until the
pipeline has to live somewhere else, or in several places at once:

- **Moving between CI vendors.** A team leaving Jenkins for GitHub Actions, or
  Bamboo for GitLab, rewrites every pipeline by hand. Each rewrite is a chance to
  lose a retry, a branch filter, a test report, or an approval gate without
  anyone noticing until it matters.
- **Several CI systems in one organization.** After an acquisition or a
  reorganization, the same company often runs GitHub Actions, GitLab, Azure
  DevOps, and Jenkins side by side. The same "test, build an image, push it,
  deploy to staging" pipeline gets written four times and drifts four ways.
- **Platform teams standardizing pipelines.** A platform or DevOps team wants one
  reviewed shape for how services are tested and shipped, but the teams it
  supports sit on different CI systems.
- **Vendor lock-in.** When the pipeline exists only in one vendor's syntax,
  switching is expensive, and that cost weakens your position in pricing and
  roadmap conversations.
- **Consultancies and tool authors.** People who deliver pipelines to clients, or
  ship a project that users build on their own CI, need the same pipeline in
  whatever system the other side uses.

Converters exist, but a converter that quietly drops what the target cannot
express is worse than no converter: the output looks complete and is not.

## What Renvoy does differently

**It refuses to lose behavior silently.** Every provider declares a capability
matrix, with each portable concept marked `native`, `emulated`,
`provider-managed`, or `unsupported`. If your pipeline uses something a provider
cannot express, such as job retries on Azure DevOps, Renvoy reports an error with
the file, line, column, and a suggested fix, and writes nothing. Emulated and
provider-managed mappings are reported as warnings or info, so you see every
place where the target behaves differently. `renvoy capabilities --provider
<name>` prints the matrix, and [providers.md](providers.md) documents each
difference.

**It writes files a person would write.** Output uses each provider's own
constructs: `needs` on GitHub and GitLab, deployment jobs on Azure, `post {
always { junit } }` on Jenkins, manual stages on Bamboo. Output is deterministic,
so the same input renders byte-identical files, and golden tests pin that. The
generated files are ordinary pipeline files that you can read, review, and keep
using without Renvoy.

**It protects hand edits.** Generated files carry a checksum and a custom block
that survives regeneration. If someone edits a generated file outside that
block, `renvoy generate` refuses to overwrite it and points you to `renvoy diff`.
`--force` replaces it but saves the edited file as a `.renvoy-backup` first. All
selected providers are validated and every write is planned before anything
touches disk, so one failing provider means no files change.

**It treats untrusted input carefully.** Values such as branch names reach
scripts through environment variables rather than being pasted into script
text, so a crafted branch name is not evaluated as a provider expression or as
shell code. This was tested through a multi-round security review, and each
issue found was fixed and pinned by a regression test. That reduces risk; it
does not remove it, and you should still review generated files like any CI
change.

**It is local-first.** One binary, no account, no service, no telemetry, and no
network access. It reads a file and writes files.

**Its releases are verifiable.** Release binaries come with SHA-256 checksums, a
Sigstore keyless signature, and GitHub build provenance, and `go install` is
checked against the public Go checksum database. [SECURITY.md](https://github.com/amitpatole/renvoy/blob/main/SECURITY.md#verifying-a-release)
shows how to verify a download.

## Value by role

**Developers** write and review one short, provider-neutral file instead of
learning five pipeline dialects. `renvoy validate` catches mistakes such as
dependency cycles, bad references, and broken artifact flow before a pipeline
ever runs, with errors that point at the exact line.

**Platform and DevOps teams** can define a standard pipeline once and generate it
for whichever CI system each team uses. `renvoy diff` exits with status 5 when
the files on disk differ from what `renvoy.yaml` would generate, so a CI check
can catch drift between the source of truth and the committed pipelines.
Provider extensions and custom blocks leave room for the provider-specific parts
without forking the definition.

**Security and compliance reviewers** get pipelines whose differences between
providers are written down rather than discovered later: every emulated or
provider-managed behavior shows up as a diagnostic. Secret values never appear
in `renvoy.yaml`, which only names secrets. The GitHub output starts from
`contents: read` token permissions and asks you to grant more explicitly. The
tool itself runs offline, and its releases can be verified end to end.

**Engineering leadership** gets a lower cost of switching CI vendors and of
running several at once, and a single reviewable definition of how software is
built and shipped. That makes CI vendor choice a decision you can revisit rather
than a one-way door.

## When not to use Renvoy

Renvoy is the wrong choice, or not yet the right one, if:

- **You use one CI system and plan to stay on it.** Writing that system's syntax
  directly is simpler, and you get every vendor-specific feature without a
  layer in between.
- **Your pipelines depend heavily on vendor-specific features.** Renvoy covers a
  portable core: triggers, stages and jobs with dependencies, conditions,
  runners, containers, matrices, timeouts, retries, environments and approvals,
  variables, secret references, and steps for checkout, scripts, caches,
  artifacts, test results, image build and push, and Kubernetes deployment.
  Anything beyond that goes in provider extensions or custom blocks, and not
  every concept maps to every provider (for example, `jobs.matrix` and
  `jobs.timeoutMinutes` are unsupported on Bamboo, and `steps.when` on GitLab and
  Jenkins).
- **You need the configuration format to be stable.** The format is
  `renvoy.dev/v1alpha1`, and it may change before `v1`.
- **You expect everything to live in the file.** Some behavior is configured in
  the provider, not in the pipeline file. On Jenkins, push, tag, and pull request
  triggers and branch filters are set on the multibranch pipeline; on Bamboo,
  secrets are managed in Bamboo. Renvoy tells you about each one, but you still
  set them up.
- **You want automatic conversion of existing pipelines.** Renvoy generates
  pipelines from `renvoy.yaml`; it does not import existing workflow files, so
  adoption starts with writing that file.
- **You need a provider that is not built in.** External provider plugins are
  designed but not implemented yet ([plugins.md](plugins.md)).

One more limit to weigh: output is checked against each provider's
documentation and with local parsers and compilers (YAML parsing, Groovy
compilation for Jenkinsfiles, javac and Bamboo's offline validation for Java
Specs), but it has not been run end to end on live GitHub, GitLab, Azure DevOps,
Jenkins, or Bamboo servers. Try generated pipelines on a branch before relying
on them.

## Try it in five minutes

Install a [signed release binary](https://github.com/amitpatole/renvoy/releases/latest),
or with Go 1.27 or newer:

```bash
go install github.com/amitpatole/renvoy/cmd/renvoy@v0.1.0
```

Then, in an empty directory:

```bash
renvoy init                              # write a commented starter renvoy.yaml
renvoy validate                          # check it without choosing a provider
renvoy generate --all --dry-run          # print every provider's output, write nothing
renvoy capabilities --provider jenkins   # see what a provider supports
renvoy generate -p github-actions -p gitlab
```

Edit a generated file and run `renvoy generate` again to see edit protection,
then `renvoy diff` to see what changed. The [configuration
reference](specification.md) covers the full format.
