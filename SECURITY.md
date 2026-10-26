# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub's
[private vulnerability reporting](https://github.com/amitpatole/renvoy/security/advisories/new)
for this repository. Do not open a public issue.

Include what you found, how to reproduce it (a `renvoy.yaml` is ideal), and the
impact you expect. You should get an acknowledgement within five working days and
a plan for a fix after the report is triaged. Fixed issues are credited in the
release notes unless you ask otherwise.

## Supported versions

Renvoy is pre-release. Until `v1.0.0`, only the latest release (or `main` before
the first release) receives security fixes.

## Scope

Renvoy reads a pipeline file and writes CI configuration. Reports we especially
want:

- Generated files that run code the `renvoy.yaml` author did not write, for example
  a value that escapes its quoting in YAML, Groovy, Java, or shell output.
- Generated files that let untrusted input (such as a pull request branch name) be
  evaluated as a provider expression or as shell code.
- `renvoy generate` writing outside the output directory, following symbolic links,
  or replacing edited files without `--force`.
- Resource exhaustion when reading a pipeline file.

Renvoy never needs secret values; pipelines only name secrets. Generated pipelines
run on your CI system with the permissions you give them; review generated files
before merging them, as you would any CI change.
