# ADR 0001: A provider-neutral IR and an executable plugin boundary

- Status: accepted
- Date: 2026-10-26

## Context

Renvoy turns one pipeline definition into native files for many CI/CD systems.
The obvious shortcut is a directory of templates per provider. Templates make the
first provider fast and every later concern hard: each template re-implements
validation, references, and escaping; differences between providers are discovered
by users in production; and nothing stops a template from quietly dropping a field
it does not understand.

The product promises portable intent, native output, and no lossy conversion.
Those promises need one place where the meaning of a pipeline is defined and
checked, and a uniform way for each provider to say what it can and cannot express.

Providers will also come from outside this repository (Harness, CircleCI, internal
systems). They need an extension point that does not tie them to Renvoy's Go
version or process.

## Decision

1. **Compile through a typed, provider-neutral IR.** `renvoy.yaml` is decoded into
   `internal/pipeline` types, validated, and normalized by `internal/compiler`
   (dependency graph, order, artifact flow). The IR has no dependency on any
   provider.
2. **Providers declare capabilities.** Each provider returns a `CapabilitySet` that
   rates every portable concept as native, emulated, provider-managed, or
   unsupported. A shared checker turns every use of an unsupported concept into a
   located error, so adapters cannot drop behavior by omission. Adapters add their
   own checks for finer limits.
3. **Rendering is structured.** YAML output is built as ordered node trees, not
   text templates. Non-YAML formats (Jenkinsfile, Java) use small code writers with
   tested escaping.
4. **External providers are executables.** A future plugin is a program that speaks
   versioned JSON messages over stdin and stdout (see [plugins](../plugins.md)).
   The core keeps headers, checksums, custom blocks, and file writing, so plugins
   cannot bypass overwrite safety.

## Consequences

- Adding a provider means implementing one interface and golden tests; the parser,
  validation, graph checks, and file safety come for free.
- The IR must stay small and honest: a concept enters it only when at least the
  built-in providers can express it or explicitly diagnose it.
- Some providers need emulation to preserve meaning (for example inherited
  conditions on GitLab and Jenkins). That logic lives in adapters, documented in
  [providers](../providers.md).
- Go plugins (`plugin` package) and in-process loading are ruled out: they require
  identical Go toolchains, do not work on Windows, and give plugins full access to
  the host process. Executables can be written in any language and isolated with
  ordinary OS tools.
- The plugin protocol is experimental while the configuration is `v1alpha1`.
