# Contributing to Renvoy

Thanks for helping. This guide covers setup, the checks every change must pass,
how golden tests work, and how to add a provider.

## Setup

- Go 1.27 or newer (the exact toolchain is pinned in `go.mod`; `go` downloads it).
- [golangci-lint](https://golangci-lint.run) v2.14.0, used for linting and formatting.
- Optional, for Bamboo Java Specs checks: JDK 21 and Maven 3.9.

```bash
git clone https://github.com/amitpatole/renvoy && cd renvoy
make build          # bin/renvoy
```

## The gate

A change is ready when `make check` passes. It runs what CI runs:

```bash
make fmt-check      # golangci-lint fmt --diff (gofmt + goimports)
make lint           # golangci-lint run
make test           # go test -race ./...
make examples       # validate and generate every example for every provider
```

CI also builds and tests on Linux, macOS, and Windows, and compiles the generated
Bamboo Java Specs with Maven. To run that locally:

```bash
RENVOY_BAMBOO_JAVA_IT=1 go test -run TestJavaSpecsCompileAndValidate -v ./internal/providers/bamboo/
```

## Golden tests

Each directory in `testdata/golden/` holds a `renvoy.yaml`, the exact files every
provider generates for it, and a `<provider>.diagnostics.txt` with that provider's
diagnostics. `TestGolden` regenerates everything, compares bytes, and also renders
twice to prove output is deterministic.

When you change rendering on purpose:

```bash
make golden                 # rewrites testdata/golden
git diff testdata/golden    # review every change before committing
```

Add a golden case when you add a feature: a valid pipeline for the happy path,
and cases in `kitchen-sink` or a new directory for diagnostics.

The capability matrix in `docs/providers.md` is generated from the provider code.
After changing a capability, run
`go test ./internal/providers -run TestProvidersDocMatrix -update`.

## Adding a provider

1. Create `internal/providers/<name>/` implementing `providers.Provider`:
   `Name`, `DisplayName`, `Description`, `Capabilities`, `Validate`, `Render`.
2. Declare a capability for every concept in `providers.AllCapabilities`. Start
   `Validate` with `providers.CheckCapabilities(p, provider)`; it turns every use of
   an unsupported concept into a located error. Add checks for finer limits.
3. Render from the normalized IR only. Read `p.Order` and `s.JobOrder` for
   ordering, `j.Needs` for dependencies, and `shared.InheritedConditions` if the
   provider does not skip dependents of skipped jobs.
4. Pass references to scripts through environment variables
   (`shared.Command` with an `envName` mapping), never as provider expressions in
   script text.
5. Build YAML with `shared.Map`; for other formats write a small writer and test
   its escaping with a golden case.
6. Include a custom block (`render.CustomBlock`) where the format allows one.
7. Register the provider in `internal/providers/registry`, document its extensions
   and differences in `docs/providers.md`, and add golden output with `make golden`.

The pipeline packages (`internal/pipeline`, `internal/compiler`) must never import
a provider package.

## Changing the configuration format

Update the Go types in `internal/pipeline`, the validator, the JSON Schema in
`schemas/` (`TestSchemaMatchesModel` checks they agree), `docs/specification.md`,
and `CHANGELOG.md`.

## Commits and pull requests

Keep commits focused and explain why a change is needed. Every pull request needs
a green CI run and, for behavior changes, tests and documentation in the same
change.

## Releasing

1. Move the entries under "Unreleased" in `CHANGELOG.md` to a new
   `## [X.Y.Z] - YYYY-MM-DD` heading, and set the same version and date in
   `CITATION.cff` and `.zenodo.json` (including the `pkg.go.dev` link).
   `TestReleaseMetadataInSync` fails until all three agree.
2. Tag `vX.Y.Z` on a green `main` and push the tag.
3. The [release workflow](.github/workflows/release.yml) runs the tests, builds
   `renvoy` for linux, darwin, and windows on amd64 and arm64 with `-trimpath` and
   `-ldflags "-X github.com/amitpatole/renvoy/internal/app.Version=vX.Y.Z"`, writes
   SHA-256 checksums, signs them with Sigstore keyless signing, records build
   provenance, and publishes a GitHub release with the CHANGELOG section as notes.
   It uses only the workflow's own `GITHUB_TOKEN` and OIDC identity; no stored
   credentials are needed.
4. The GitHub release triggers Zenodo, which archives it and mints a version DOI.
5. `go install github.com/amitpatole/renvoy/cmd/renvoy@vX.Y.Z` works as soon as the
   tag is pushed.

Workflow actions are pinned to commit SHAs; Dependabot proposes updates.

## Code of conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md).
