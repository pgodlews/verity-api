# Load testing

`verity load` runs the cases in a Verity project from many virtual users and
reports latency, throughput, and error rate. It executes each case through
the same runner as `verity run` (assertions, transfers, auth, redaction),
so a load script is a normal project file.

```bash
verity load project.yaml --env local --env-file .env \
  --var region=eu-west --timeout 10s \
  --vus 20 --duration 30s --ramp-up 5s --think 50ms \
  --threshold 'p95<500ms' --threshold 'GET widget:p95<300ms' \
  --max-error-rate 1% --json load.json
```

Pass `--duration` or `--iterations`, not both.

`verity load` accepts the same environment and variable flags as `verity run`:

- `--env NAME` selects the environment (`default: true` when omitted).
- `--suite GLOB` and `--case GLOB` filter which suites and cases run.
- `--env-file FILE` (repeatable) loads a dotenv file into the process
  environment before the project is resolved (later files win; existing process
  variables win unless `--env-file-override` is set).
- `--var k=v` (repeatable) sets or overrides a variable at CLI precedence
  (above environment, suite, case, and data-feed rows).
- `--timeout DURATION` sets the per-request HTTP timeout (default `30s`; a
  step's own `timeoutMs` still overrides it).
- `--plugins FILE` loads an explicit plugin manifest instead of
  `<project-dir>/.verity/plugins.json`.
- `--trust-plugins` runs the plugins in `<project-dir>/.verity/plugins.json`
  (or set `VERITY_TRUST_PLUGINS=1`); without it such a project is refused.

## What one iteration is

A virtual user walks the selected cases in project order, then starts again
at the first case. **One iteration is one case run**, not a pass over every
case. With two cases and `--iterations 3`, each user runs case A, case B,
case A.

`--iterations` is per virtual user. Three users and `--iterations 4` produce
12 case runs. Inside a case, steps still run in order, so a transfer from
step 1 is visible to step 2 of that same run. A data feed, when the case has
one, expands to all of its rows inside that single iteration.

Each virtual user keeps its own variables. A `scope: run` transfer made
during the load is visible to later iterations of that user only.

## Setup, once

Suite `setup` hooks of the selected suites run **once**, before any virtual
user starts. `scope: run` captures from those hooks are the baseline every
user starts from (a login, for example). They are not repeated and they are
not part of the latency or RPS numbers. Teardown hooks are not run.

Put a one-shot login in the suite setup hook. A case in the load loop runs
once per iteration, which is what you want for the traffic itself.

## Duration, ramp-up, think time

`--duration 30s` is how long users keep **starting** new iterations after
they are released. An iteration that has already started finishes. Ramp-up
happens inside that window: user `i` of `N` waits `i * ramp-up / N` before
its first iteration, but not longer than the time left in `--duration`.
A user whose start time is already past the deadline does not run.

`--think 50ms` is a pause between iterations of one user. It is not included
in the latency figures. Ramp-up and think time are capped at the time left
in `--duration`, and both still end early when the run is cancelled.

`--vus` defaults to 1, must be at least 1, and is capped at 1000. Above 200
the CLI prints a warning and still starts.

## Safety

Before any request, Verity resolves every selected request `url` and `target`
and refuses the run unless each host is:

- IPv4 or IPv6 loopback
- RFC1918 private IPv4 and IPv6 unique-local (`fc00::/7`)
- IPv4 or IPv6 link-local unicast

IP literals are checked as addresses. They are not dialed when the run is
refused. Other names are looked up; every returned address must be in the
list above. An unresolvable name, or a URL that still contains `${...}`, is
refused the same way. `unix:` targets are local and skipped.

Each selected case's data feed is expanded first. `url` and `target` are
interpolated per row at the same precedence as a case run (environment, run
captures, suite, case, the row, CLI vars, then step vars). A row that fails
to load fails the screen (exit 3).

`dns://authority/host:port` and `passthrough://authority/endpoint` dial the
host in the path. That path is screened, and so is a non-empty authority
(the resolver may contact it). `dns:///host:port` and
`passthrough:///host:port` still screen the path.

An out-of-process protocol is screened on `target`, `url`, `address`,
`endpoint`, and `host`. The dial guard cannot see sockets that process
opens. A plugin step with none of those fields is refused unless
`--allow-remote` is set; the message names the step.

The same allow-list is applied again at connect time, unless
`--allow-remote` is set. Every HTTP connection the load run opens — the
shared pool, a private TLS transport, and an OAuth2 token request — is
dialed through a guard. The guard checks the IP that would be connected,
including a redirect target, a host a feed row substituted, and a host a
transfer captured at runtime. While the guard is active, `HTTP_PROXY` and
`HTTPS_PROXY` are ignored: a proxy would hide the real destination from the
guard, so every request connects directly.
A name is resolved at dial time, so a DNS change after the screen is caught.
A refused dial is a transport error
(`refusing non-private address <address>; pass --allow-remote`). The
first refusal stops the run and the process exits 3.

`--allow-remote` turns both checks off. The CLI prints the target hosts, the
VU count, and the duration or iteration count before the first virtual user
starts, and it says that non-private targets are permitted. That line is
printed for local targets too.

## Metrics

Per step name, and once for the whole run:

| Field | Meaning |
|-------|---------|
| requests | steps that ran (passed, failed an assertion, or errored) |
| assertion failures | steps whose status is `failed` |
| transport errors | steps whose error code is `transport_error` |
| other errors | steps that errored for another reason (config, auth) |
| error rate | (assertion + transport + other) / requests |
| RPS | requests / load-window seconds |
| latency | min, mean, p50, p90, p95, p99, max of the protocol call, in milliseconds |
| step latency | the same statistics for the whole step |
| status codes | response status counts |
| errors | the first 10 distinct failure messages |

Skipped and aborted steps are not requests. Latency is the protocol call
of the step's final attempt (`spi.Response.Duration`: the network exchange),
recorded in microseconds, so client-side work does not inflate it. A step
that got no response (a transport error) counts its step time instead.
Step latency (`stepLatency`) is the runner's whole step: every attempt and
retry delay plus interpolation, auth, redaction, assertions, and transfers.
Latency thresholds use latency, not step latency. A load run keeps lean
captures: no request capture and only the status of each response, since
the load report shows neither. JSON prints milliseconds with three decimal places. The
console table prints two decimal places; the live line prints one. Min,
mean, and max are the raw samples, including ones longer than an hour.
Percentiles come from a fixed log-bucket histogram (about 1% bucket width,
origin 1µs, saturating at one hour) so a long run does not keep every
sample. A percentile can sit up to about one bucket above the true sample
and is never below it.

The same step name in two cases is one row. Thresholds refer to that name.

On a terminal, a line `vus=… rps=… p95=…ms errors=…` is rewritten once a
second. It is not printed when stdout is not a terminal.

## Thresholds and exit codes

```text
p50|p90|p95|p99|max|mean <|<= <number><ms|s>
<step name>:p95<300ms
```

`--max-error-rate` accepts `1%` or a fraction from 0 to 1 (`0.01`). A
percentage outside 0–100 is a usage error. The run fails when the actual
rate is **greater** than the maximum; an equal rate passes. If a latency
threshold or `--max-error-rate` is set and no request ran — overall, or for
the named step — that check fails (`no requests ran`, or `step did not run`
when the step is named). A skipped step is not a request.

| Code | When |
|------|------|
| 0 | every threshold passed, and the error rate is within the maximum if one was set |
| 1 | a threshold or the error rate failed, or a setup hook failed (the load does not start) |
| 2 | usage: bad flags, VU cap, both or neither of `--duration` and `--iterations` |
| 3 | the project did not load or validate, or a target was refused |
| 4 | the report could not be written |
| 5 | interrupted (SIGINT/SIGTERM). The report is still written |

With no `--threshold` and no `--max-error-rate`, exit code 0 does not mean
every request succeeded. The summary still counts failures. Set a threshold
when CI should fail the job.

Ctrl-C or SIGTERM stops users from starting another iteration, waits for the
request already in flight, writes the report, and exits 5. A second signal
exits immediately, the same way `verity run` does.

## Connections

Every virtual user shares one `http.Transport` (keep-alive, idle pool sized
to the VU count). Each attempt still builds its own `http.Client`, so the
per-step timeout stays on the client and idle connections stay in the pool.
A step that brings its own TLS config (mTLS) gets a private transport,
cached for the run and keyed by the client cert, CA, insecure flag, and
server name, so certificates are not reused for other steps and idle
connections are not rebuilt on every attempt. The shared pool and that
cache are closed when the run finishes. Unless `--allow-remote` is set,
those transports (and OAuth2 token requests) dial through the connect-time
guard above. Pass a transport of your own only when embedding `core/load`.
Without `AllowRemote` it must be an `*http.Transport` with no dialer of its
own; the guard is installed on it and its proxy is turned off. Any other
transport is refused with a `config_error`.

Plugins are loaded the same way as `verity run`: `--plugins FILE`, or
`<project-dir>/.verity/plugins.json` when that file is present and trusted
(`--trust-plugins` or `VERITY_TRUST_PLUGINS=1`).

## JSON report

`--json FILE` writes a redacted document. `schemaVersion` is `"1"`.

```json
{
  "schemaVersion": "1",
  "project": "widgets",
  "environment": "local",
  "startedAt": "2026-01-01T00:00:00Z",
  "finishedAt": "2026-01-01T00:00:30Z",
  "durationMs": 30000,
  "interrupted": false,
  "vus": 20,
  "iterations": 1840,
  "rampUpMs": 5000,
  "thinkMs": 50,
  "configuredDurationMs": 30000,
  "targets": ["<target-host>"],
  "summary": {
    "requests": 1840,
    "assertionFailures": 0,
    "transportErrors": 0,
    "errorRate": 0,
    "rps": 61.3,
    "latency": {
      "minMs": 4.210, "meanMs": 12.400, "p50Ms": 11.200,
      "p90Ms": 20.100, "p95Ms": 28.500, "p99Ms": 40.800, "maxMs": 55.000
    },
    "stepLatency": {
      "minMs": 4.900, "meanMs": 13.300, "p50Ms": 12.000,
      "p90Ms": 21.200, "p95Ms": 29.700, "p99Ms": 42.100, "maxMs": 57.400
    },
    "statusCodes": [{"code": 200, "count": 1840}],
    "errors": []
  },
  "steps": [],
  "thresholds": [
    {"expr": "p95<500ms", "passed": true, "metric": "p95", "actualMs": 28.500, "limitMs": 500.000}
  ],
  "errorRateThreshold": {"max": 0.01, "actual": 0, "passed": true},
  "passed": true
}
```

`durationMs` is the load window (first user released through the last user
finished), not including setup. `iterations` is case runs that finished,
including ones that failed an assertion. Latency fields are milliseconds
with three decimal places (microsecond resolution). `errors` is always
present (`[]` when nothing failed). `actualMs` and `limitMs` are always
present on a threshold. `errorRate` and `errorRateThreshold.max` /
`.actual` are fractions (`0.01` is 1%).
`configuredIterations` is present in iteration mode instead of
`configuredDurationMs`. `setup` lists setup-hook steps when the suite has
them. `setupFailed` is true when a hook failed and virtual users did not
start.

Every string is passed through the run's redactor before it is written.
The console summary is redacted the same way.

## Desktop app

The **Load test** view (next to **Test runs** in the left rail) runs the same
`core/load` engine over the open project:

- Pick suites and cases (all run when none are checked), virtual users
  (1–1000, with the same >200 warning as the CLI), duration or iterations per
  virtual user, ramp-up, think time, latency thresholds (`p50`, `p90`, `p95`,
  `p99`, `max`, `mean`, overall or scoped to a step via the step selector), and
  max error rate.
- Target hosts for the selected suites, cases, and active environment are shown
  before the run starts (`LoadTargets`). Non-private hosts are refused with the
  same message as the CLI (`refusing non-private target <host>; pass
  --allow-remote to run it`), and **Start load test** stays disabled while a
  target is refused.
- **Allow remote targets** (`--allow-remote`) is behind an explicit checkbox
  with a warning sentence. It is never remembered between runs: starting a run
  or switching projects/environments resets it to unchecked.
- While a load test is running, `StartLoad` streams `load:live` events about
  once a second with active VUs, RPS, p95 latency, and error count
  (`aria-live="polite"`). **Cancel** (`CancelLoad`) stops the run like Ctrl-C,
  waits for in-flight requests to finish, and returns the partial report
  (`interrupted: true`).
- When the run finishes, the view shows threshold pass/fail badges, the
  per-step table (`requests`, `failures`, `errors`, `error rate`, `RPS`, and
  `min`/`mean`/`p50`/`p90`/`p95`/`p99`/`max` latency), status code counts, and
  **Save JSON report** (`SaveLoadReport`). Every string in the in-memory and
  saved report is redacted by `core/load` before it reaches the frontend.

The previous report remains available if new settings are invalid or starting a
run fails. During a new run its results are hidden; the completed or interrupted
report replaces them when returned. Switching or closing projects clears reports.
