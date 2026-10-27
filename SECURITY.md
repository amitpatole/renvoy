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

## Verifying a release

Release binaries are built by the
[release workflow](.github/workflows/release.yml) from the tagged commit. Nobody
holds a long-lived signing key: the workflow signs `checksums.txt` with Sigstore
keyless signing and records build provenance, both bound to this repository's
workflow identity and logged in Sigstore's public transparency log.

To check a download (replace the version and archive name):

```bash
# 1. The checksum file was signed by this repository's release workflow for this tag.
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/amitpatole/renvoy/.github/workflows/release.yml@refs/tags/v0.1.0" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

# 2. The archive matches the signed checksum.
sha256sum --ignore-missing -c checksums.txt

# 3. GitHub build provenance for the archive (optional, needs the gh CLI).
gh attestation verify renvoy_0.1.0_linux_amd64.tar.gz --repo amitpatole/renvoy
```

`go install` does not use these files. It downloads source through the Go module
proxy and checks it against the public Go checksum database (sum.golang.org), so
a tagged version cannot be changed after it is first recorded.

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
