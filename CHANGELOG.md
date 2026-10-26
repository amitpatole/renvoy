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
