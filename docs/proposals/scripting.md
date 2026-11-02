# Proposal: embedded scripting for steps and feeds

Status: proposed. Roadmap §5; no implementation or dependency is included here.

## Goal and scope

Allow small scripts where Verity's declarative model cannot express a request or check, without turning projects into general-purpose programs. A project obtained from a repository is **untrusted code**: reading or validating it must never execute a script.

The useful cases are:

- **Pre-request:** computed headers and bodies, request-specific timestamps/nonces, canonicalization, and signatures not covered by a provider. Run after interpolation, protocol request building, and upload finalization, but before message authentication so SigV4/HMAC sign the final bytes.
- **Post-response:** compound checks, comparisons across fields, and extraction that needs transformation rather than a plain path. Results become ordinary assertions and variables.
- **Feed shaping:** normalize a row, derive fields, or join values before variable interpolation. Filtering and large data pipelines remain data-source plugin work.
- **Migration:** retain useful Postman pre-request/test behavior. The importer currently converts common test idioms and warns for every pre-request script and unconverted test line; it must not silently begin executing imported code.

Keep using native features when they suffice: `jsonpath`, `regex`, schema and other assertions for checks; transfers for extraction; `${fn:timestamp}`, `${fn:uuid}`, and related interpolation for common dynamic values; HMAC and SigV4 providers for their existing signature schemes. Scripting is not a second plugin SDK, template language, or Node.js runtime.

## Engine decision

Evidence below is labelled **[read]** (opened upstream source) or **[reasoned]** (inference, including unmeasured cost). No startup benchmark was run.

### goja

- **[read]** The [repository](https://github.com/dop251/goja) describes a pure-Go implementation with full ES5.1 and most ES6, the latter still in progress—not complete modern JavaScript. It documents `Runtime.Interrupt`, and says a runtime is confined to one goroutine. Its [licence](https://raw.githubusercontent.com/dop251/goja/master/LICENSE) is MIT. The current README requires Go 1.26 and describes continuing ES6 work.
- **[reasoned]** A fresh runtime per invocation should have modest startup cost and avoids shared-state bugs; a bounded pool fits parallel cases. Interrupt supplies a CPU deadline, but the opened documentation offers no per-runtime heap cap, so an in-process script cannot receive a hard memory limit. Date, randomness, and host objects must be controlled for reproducibility. With no Node globals or host capabilities installed, scripts have no ambient filesystem, network, process, or environment API.
- **[reasoned]** JavaScript gives the best Postman familiarity and the smallest importer shim. Pure Go preserves non-cgo cross-compilation. This compatibility outweighs incomplete newer-ES support; supported syntax must be tested and documented rather than called “ES2020 compatible.”

### yaegi

- **[read]** The [repository](https://github.com/traefik/yaegi) describes a pure-Go Go interpreter, with `unsafe` and `syscall` neither used nor exported by default. Its API includes context-aware evaluation. The [licence](https://raw.githubusercontent.com/traefik/yaegi/master/LICENSE) is Apache-2.0, and the README states support for the latest two major Go releases.
- **[reasoned]** Parsing/interpreting Go and bridging reflected values is likely more startup and implementation cost than the small goja object model. No per-interpreter memory ceiling is claimed here. A no-stdlib policy reduces ambient capabilities, but Go's goroutines, maps, clocks, and random sources would need restriction for deterministic scripts. Go syntax helps Go developers but gives Postman users no migration benefit.

### Starlark in Go

- **[read]** The [repository](https://github.com/google/starlark-go) describes an embeddable Python dialect and a 3-clause BSD [licence](https://raw.githubusercontent.com/google/starlark-go/master/LICENSE). Its [language specification](https://raw.githubusercontent.com/google/starlark-go/master/doc/spec.md) says core execution is deterministic. The README documents execution-step limits and asynchronous cancellation, explicitly says thread memory cannot be bounded, and recommends an OS-limited separate process for denial-of-service defense. It also says interpretation starts quickly and tracks two recent Go releases.
- **[reasoned]** This is the strongest determinism and resource-accounting alternative, with low per-step startup and straightforward non-cgo Go builds. Host-defined built-ins provide a narrow sandbox. However, Python-like syntax and a new assertion idiom would make Postman compatibility substantially more expensive.

### WebAssembly: wazero and Extism

- **[read]** [wazero](https://github.com/tetratelabs/wazero) is a zero-dependency, non-cgo Wasm runtime with compiler and interpreter modes, cross-compilation support, memory-page limits, close-on-context cancellation, and compilation caching. It has an Apache-2.0 [licence](https://raw.githubusercontent.com/tetratelabs/wazero/main/LICENSE) and an API stability policy. [Extism](https://github.com/extism/extism) adds host functions, limits/timers, and optional HTTP/filesystem capabilities; its [licence](https://raw.githubusercontent.com/extism/extism/main/LICENSE) and the [Go SDK licence](https://raw.githubusercontent.com/extism/go-sdk/main/LICENSE) are BSD-3-Clause. The Go SDK documents wazero underneath and per-instance state.
- **[reasoned]** Cached module instantiation is attractive, but initial compilation and JS-to-Wasm packaging add per-step and toolchain cost. Linear-memory limits and explicit host imports give strong isolation; deterministic behavior still requires controlled clock/random/network imports. Wasm suits compiled plugins, not a short JavaScript source field: a JS engine guest plus a `pm.*` bridge is too much machinery for this feature.

**Decision (reasoned): use goja**, behind an interface that could admit another engine later. Do not claim a hard in-process memory sandbox. The first-party implementation runs in isolated, persistent workers; the base binary does not initialize goja when a project has no scripts.

## Sandbox, approval, and secrets

The script global object contains only the documented bridge. There is no `require`, module loader, eval of host Go values, filesystem, socket, subprocess, timer, or process-environment access. Filesystem/network capabilities may be added later only as explicit per-script grants, routed through host policy (including load-test dial guards); process execution should remain unavailable. Grants are part of approval.

Each inline/file script has `timeoutMs` (default 250 ms, maximum 5 s) and `memoryMb` (default 64 MiB, configurable within a host maximum). A lazy worker pool is bounded by case parallelism; each worker executes one script at a time with a fresh goja runtime. The parent interrupts on deadline or run cancellation and kills a worker that does not exit promptly. Before execution, each worker receives an OS-enforced memory/address-space limit appropriate to the platform; exceeding it is `script_memory_exceeded` and the worker is replaced. If a supported platform cannot enforce the limit, scripting fails closed there rather than silently becoming unlimited. Input, output, source (256 KiB), log (64 KiB), and mutation sizes are also capped. These are design requirements, not claims of existing goja features.

CLI execution requires `--allow-scripts`; CI should instead pin `--script-approval sha256:…`. Validation, formatting, import, scanning, and schema commands never execute code. The desktop app calculates SHA-256 over ordered inline source, external file bytes, engine/version, grants, limits, and inheritance context. It prompts before running a changed set and shows the affected scripts and grants. Approval lives in local application state, keyed to the project, **never in the repository**. Rejecting approval skips execution with a structured error; editing any covered byte invalidates it.

`vars` contains ordinary resolver values, not ambient environment variables. `env.get("NAME")` is exactly a strict `${env:NAME}` resolution; `env.secret("provider:key")` is exactly `${secret:provider:key}`. Names must be declared in a script's `secrets` grant, resolution goes through `core/secrets`, and values immediately enter the redactor. There is no enumeration. Script source itself is not interpolated.

Exact JavaScript taint tracking is not trustworthy across coercion and host calls. Use conservative propagation: after the first secret read, every subsequent script-written request field, variable, log entry, and assertion value is treated as secret. Requests still send the bytes, but captures mask those fields; variable displays are masked; post-secret logs are masked wholesale. Earlier unaffected fields remain readable. Existing response-secret registration and final recapture still apply. This deliberately over-redacts rather than risking a derived token.

`verity scan-secrets` should scan inline and `file:` bodies. Extend its existing high-confidence token/PEM text scan with JavaScript credential-assignment checks, report value-free paths/lines, and test false positives. External script files join validation, formatting preservation (never reformat JS), secret scans, trust hashes, and `--watch`.

## API and execution semantics

The versioned `verity` bridge exposes:

- `request` (pre only, mutable): `method`, `url`, `headers`, `query`, `cookies`, `body`/`bodyBase64`, and JSON-safe `config`, mapping directly to `spi.Request`. TLS is never exposed. Header helpers preserve multi-value semantics.
- `response` (post only, read-only): `statusCode`, `status`, `headers`, `body`/`bodyBase64`, `durationMs`, `contentType`, `meta`, and `json()`, mapping to `spi.Response` without replacing raw captures.
- `vars.get/set/unset`, string-valued, with case scope by default and explicit `scope: "run"`; `feed` scripts instead receive and return one string map as `row`.
- `env.get/secret`, `assert(value, message)`, and `expect(value).toEqual/toContain/toExist`. `console.log/info/warn/error` returns structured entries through the redactor, never process stdout directly.
- A small `crypto` host module for SHA-256, HMAC-SHA256, hex/base64, UUID, and random bytes. Clock and randomness come from resolver-injected sources, allowing deterministic tests. No arbitrary crypto or network package is imported.

Per attempt the order is: interpolate → build → finalize body → suite/case/step **pre** (outer to inner) → resolve/apply auth → execute → **post** (inner to outer) → declarative assertions → retry decision. Thus pre-script changes are signed, and post failures participate in `retry.until: passed`. Script variable mutations use an attempt overlay; only the final attempt commits them, then existing transfers run from that response. Syntax/runtime/timeout/limit errors are `script_error` (or a stable limit subcode), not retryable transport errors. Assertion failures are ordinary failures.

The `pm.*` subset covers `pm.request`, `pm.response` (`code`, `status`, `responseTime`, `headers`, `text()`, `json()`), common status/header chains, `pm.test`, the listed `pm.expect` matchers, and `pm.variables`, `environment`, `collectionVariables`, and `globals` get/set/unset. Verity case/run scopes replace Postman's exact scope behavior where necessary and are documented. Warn about `pm.sendRequest`, cookies as a persistent jar, `pm.execution`, vault, visualizer, mock/dataset/state APIs, `pm.require`, arbitrary Chai, async APIs, and CryptoJS. Import remains structural by default; an explicit `--scripts` option retains eligible source and emits a warning for every unsupported call rather than silently dropping it.

## Project and runner integration

Add a small `StepHook` SPI with pre/post contexts and typed mutation/results, plus corresponding VPP/2 methods and secret-resolution callbacks. The runner—not an auth provider or assertion—owns ordering and retry semantics. Built-in and external implementations use the same hook; the first-party goja plugin is loaded only when needed. A single current VPP client serializes calls, so workers must be pooled rather than sharing one bottleneck.

```yaml
scripts:
  pre:
    inline: |
      request.headers.set("X-Nonce", crypto.uuid())
  post:
    file: scripts/check.js
    timeoutMs: 250
    memoryMb: 64
    secrets: ["env:SIGNING_KEY"]
```

`scripts.pre/post` is valid on suite, case, and step, with exactly one of `inline` or `file`; files stay under the project directory. A feed may add `map` with the same source shape. JSON Schema and semantic validation cover source exclusivity, bounds, paths, grants, and syntax without execution. `StepResult` gains redacted script phase/name/duration/log entries; script assertions reuse `AssertionResult` with type `script`, and variable writes reuse capture display rules. JSON/JUnit/HTML and GUI events consume the same result, with a GUI script panel and no raw secret-bearing bridge data.

`verity load` uses a dedicated worker/runtime per active VU, initialized before samples, and includes each invocation in step latency. Worker memory is multiplied by VU count, so the global pool/maximum must be explicit. Benchmark startup, steady-state latency, allocation, and parallel throughput before enabling scripting in load tests.

## Milestones and tests

1. **Contract:** model/schema, `StepHook` SPI and VPP/2, approval hashes, validation/watch/scanning. Test malformed source, paths, permissions, and unchanged reports for script-free projects.
2. **Pre-request:** isolated goja workers, request bridge, crypto, limits, CLI/GUI approval. Test signing order, attempts, worker restart, cross-compilation, and load pool accounting.
3. **Post-response:** assertions, transactional variables, reports/GUI, redaction recapture. Test retry and transfer ordering.
4. **Migration and feeds:** opt-in Postman preservation/`pm.*`, then `feed.map`. Publish compatibility warnings and measured load costs.

Security tests attempt filesystem/network/process/env access, prototype and host-object escapes, huge strings/arrays, deep recursion, infinite loops and pathological regex, worker crashes, and Ctrl-C during every phase. Redaction tests transform, encode, concatenate, and log secrets and require conservative masking. Determinism tests freeze clock/randomness and compare repeated outputs; race tests cover parallel cases/VUs. Reporter golden tests cover logs/errors. Hand-authored Postman fixtures cover every supported call, each warning, secret literals, and proof that import/validate never runs source. Fuzz the source parser, bridge values, and mutation decoder.

## Open questions

- Is a first-party out-of-process goja plugin plus VPP/2 acceptable, or should the maintainer trade base-binary growth for an in-process built-in with weaker memory isolation?
- Are the default limits, fail-closed platform policy, and CLI approval flags the right UX for existing CI users?
- Is conservative post-secret-read redaction acceptable, especially masking an entire mutated body?
- Should suite/case scripts wrap every descendant step as proposed, and should feed mapping be in the first release?
- Which exact ES syntax and `pm.expect` chains are required before calling import compatibility useful?
