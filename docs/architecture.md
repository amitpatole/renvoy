# Architecture

Renvoy is a compiler. A `renvoy.yaml` file is parsed, checked, and normalized into
a provider-neutral intermediate representation (IR); each provider then checks the
IR against what it can express and renders native files.

```text
renvoy.yaml
   │  internal/config      read (1 MiB cap), discover the file
   ▼
Parser                     internal/pipeline   strict YAML decoding, source map
   │
   ▼
Field validation           internal/pipeline   names, references, steps, patterns
   │
   ▼
Normalization              internal/compiler   stage/job graph, order, artifact flow
   │                                           → normalized IR (*pipeline.Pipeline)
   ├──► Provider capability validation         internal/providers (+ each adapter)
   │         │
   │         ▼
   └──► Provider renderer ──► files            internal/providers/<adapter>
                                 │
                                 ▼
                       Header, checksum,       internal/render
                       custom blocks, safe
                       writes, diff
```

## Packages

| Package | Responsibility |
| --- | --- |
| `cmd/renvoy` | Binary entry point. |
| `internal/app` | Cobra commands, exit codes, the starter file. |
| `internal/config` | File discovery, bounded reads, the compile entry point. |
| `internal/diagnostics` | Structured errors, warnings, and info with paths and positions; text and JSON output. |
| `internal/pipeline` | The typed IR, strict decoding, the `${ns.name}` reference syntax, field validation. |
| `internal/compiler` | Dependency resolution, cycle detection, topological order, artifact producers. |
| `internal/providers` | The `Provider` interface, capability model, shared capability checks. |
| `internal/providers/shared` | YAML building, shell quoting, condition helpers used by several adapters. |
| `internal/providers/<adapter>` | One package per target: `githubactions`, `gitlab`, `azuredevops`, `jenkins`, `bamboo`. |
| `internal/providers/registry` | The list of compiled-in providers. |
| `internal/render` | Generated headers, checksums, custom blocks, overwrite safety, writing, diff. |
| `pkg/plugin` | Public, versioned message types for future external providers. |
| `schemas` | The JSON Schema for editors. |

## Invariants

1. **The IR never depends on a provider.** `internal/pipeline` and
   `internal/compiler` import no provider package. Provider extension blocks are
   kept as raw YAML nodes and decoded only by their provider.
2. **No silent loss.** Every portable concept a pipeline uses is checked against
   the provider's capability set. Unsupported concepts are errors; provider-managed
   and lossy-but-safe mappings are reported as info or warnings.
3. **Validate everything before writing anything.** `generate` validates every
   selected provider, renders every file, and plans every write before the first
   byte reaches disk. One incompatible provider or one unsafe overwrite means no
   files change.
4. **Deterministic output.** Rendering uses ordered structures, never map
   iteration; golden tests render twice and compare bytes.
5. **Never destroy user edits.** Files are replaced only when they are new or
   unedited Renvoy output (the checksum matches). Custom blocks always survive;
   malformed ones block generation even with `--force`.
6. **References never become script text.** Values reach scripts through
   environment variables, so untrusted values such as branch names are never
   evaluated as provider expressions or shell code.
7. **Diagnostics are located.** Every diagnostic carries a config path; the source
   map adds the file, line, and column.

## Decoding

`pipeline.DecodeStrict` walks the YAML node tree alongside the Go types. It reports
unknown fields (with "did you mean" hints), wrong shapes, and empty values with
exact paths, and it bounds the number of visited nodes, counting nodes reached
through aliases, so alias-expansion documents cannot exhaust resources. Only when
the shape is valid does it decode into the typed model. Providers use the same
function for their extension blocks.

## Normalization

`compiler.Normalize` assigns indexes and back-pointers, resolves `dependsOn`
(a stage without `dependsOn` follows the previous stage), detects cycles, orders
stages and jobs topologically (preferring declaration order), and computes each
job's direct dependencies: same-stage dependencies, or for a stage's entry jobs,
the final jobs of each needed stage. It also checks that every downloaded artifact
has exactly one producer upstream of the consumer.

## Rendering

YAML providers build ordered `yaml.Node` trees, so escaping and key order are
handled by the encoder. Jenkins and Bamboo Java Specs are generated with small
code writers whose string escaping is tested by golden cases; Bamboo Java output
is compiled and validated with Bamboo's own library in CI.

## Generated-file safety

`render.Finalize` prepends the header, splices in the custom block from the
existing file, and records a SHA-256 checksum of the content with the checksum
value and the custom block's inner lines removed. `render.Plan` compares that with
the file on disk:

| On disk | Action |
| --- | --- |
| nothing | create |
| identical | unchanged |
| unedited Renvoy output | update |
| edited, hand-written, or foreign | refuse (exit 4), or with `--force` save a `.renvoy-backup` copy and overwrite |
| malformed custom block, symlink, non-regular file | refuse, even with `--force` |

Writes refuse to follow symbolic links in any path component under the output
directory and replace files atomically (temporary file plus rename).
