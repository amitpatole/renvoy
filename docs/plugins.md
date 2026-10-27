# External provider plugins (proposed)

> Status: design only. The message types exist in [`pkg/plugin`](https://github.com/amitpatole/renvoy/tree/main/pkg/plugin)
> and are experimental while the configuration API is `renvoy.dev/v1alpha1`.
> Renvoy does not load plugins yet.

Built-in providers are compiled into the binary. External providers will be
standalone executables, so they can be written in any language and do not depend
on Renvoy's Go version. See [ADR 0001](adr/0001-provider-neutral-ir.md) for why.

## Intended experience

```bash
renvoy plugin install github.com/acme/renvoy-harness
renvoy generate --provider harness
```

A plugin named `harness` is an executable called `renvoy-provider-harness`.
`renvoy plugin install` will download a release for the current platform, verify
its checksum (and signature when the publisher provides one), and place it in the
user's plugin directory. Plugins are never run from the repository being built.

## Transport

The core starts the plugin with no arguments and an empty environment apart from
`PATH`, `HOME`, and `TMPDIR`. It writes one JSON request per line to the plugin's
stdin and reads one JSON response per line from its stdout. Stderr is shown to the
user as plugin logs. Each request has an `id`; the response repeats it.

The core enforces limits: a response line of at most 16 MiB, a timeout per
request, and termination of the plugin process when the session ends.

## Session

### 1. Handshake

The core lists the protocol versions it speaks; the plugin picks one.

```json
{"id":"1","type":"handshake","handshake":{"coreVersion":"v0.2.0","protocolVersions":["renvoy.dev/plugin/v1alpha1"],"configApiVersion":"renvoy.dev/v1alpha1"}}
```

```json
{"id":"1","type":"handshake","handshake":{"protocolVersion":"renvoy.dev/plugin/v1alpha1","name":"harness","displayName":"Harness","description":"Harness CI pipeline","pluginVersion":"1.0.0"}}
```

If the plugin supports none of the offered versions it replies with an `error`
response (`code: "unsupported_protocol"`) and exits.

### 2. Capabilities

```json
{"id":"2","type":"capabilities"}
```

```json
{"id":"2","type":"capabilities","capabilities":[{"id":"jobs.matrix","support":"native"},{"id":"steps.cache","support":"unsupported","note":"Harness caches are configured on the pipeline"}]}
```

Capability IDs and support levels are the ones listed in
[providers](providers.md#support-levels). The core runs its shared capability
check with this set, exactly as for built-in providers.

### 3. Validate

The core sends the normalized pipeline: the IR after validation and graph
normalization, with the plugin's extension block (`extensions.<name>`) included and
other providers' blocks removed.

```json
{"id":"3","type":"validate","pipeline":{ "...": "normalized IR" }}
```

```json
{"id":"3","type":"validate","diagnostics":[{"severity":"error","path":"stages[0].jobs[1].retries","message":"Harness cannot retry this job type","hint":"remove retries"}]}
```

Diagnostics use config paths; the core adds file positions.

### 4. Render

Sent only when no validation errors were reported for any selected provider.

```json
{"id":"4","type":"render","pipeline":{ "...": "normalized IR" },"render":{"sourceName":"renvoy.yaml"}}
```

```json
{"id":"4","type":"render","files":[{"path":".harness/pipeline.yaml","content":"pipeline:\n  ...\n","comment":"#"}]}
```

The core then does what it does for every provider: rejects unsafe paths
(absolute, `..`, backslashes), adds the generated header and checksum, keeps
existing custom blocks, and applies the overwrite rules.

### Errors

Any request can fail with:

```json
{"id":"3","type":"error","error":{"code":"internal","message":"..."}}
```

## Security model

- Plugins receive the pipeline definition only. Secrets are names, never values.
- The core owns file writes, so a plugin cannot write outside the output
  directory or overwrite edited files.
- Plugins are installed explicitly and verified; Renvoy never executes a plugin
  found in the repository.

## Versioning

The protocol version is independent of the config `apiVersion`. Additive fields do
not change it; removing or changing a field does. The normalized IR's JSON shape is
part of the protocol and will be documented with a JSON Schema before the protocol
leaves alpha.
