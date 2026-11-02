import { useEffect, useMemo, useState } from "react";
import { api, hasNativeDialogs } from "../api";
import { Alert, Check, Close, Copy, Play, Plus, Save, Shield, Stop } from "../icons";
import type { RunSettings } from "../lib/storage";
import type {
  LoadLiveStats,
  LoadMetrics,
  LoadOptions,
  LoadReport,
  LoadTargetReport,
  ProjectInfo,
} from "../types";
import { copyText, cx, errorMessage, formatMs, formatWhen } from "./parts/format";
import {
  collectProjectTargets,
  collectSelectedSteps,
  formatReportJson,
  formatThreshold,
  LOAD_METRICS,
  LOAD_OPS,
  LOAD_UNITS,
  loadCliCommand,
  refusalMessage,
  vuWarning,
  validateLoadProfile,
  type LoadProfileErrors,
  type LoadMetric,
  type LoadOp,
  type LoadUnit,
  type ThresholdDraft,
} from "./parts/load";
import { StatusMark } from "./parts/StatusMark";
import "./views.css";

export function LoadView(props: {
  info: ProjectInfo;
  environment: string;
  report: LoadReport | null;
  live: LoadLiveStats | null;
  running: boolean;
  settings?: RunSettings;
  onStart: (opts: LoadOptions) => Promise<LoadReport>;
  onCancel: () => void;
  onClearReport: () => void;
}): JSX.Element {
  const { info, environment, report, live, running, settings = {}, onStart, onCancel, onClearReport } = props;

  const [selectedSuites, setSelectedSuites] = useState<string[]>([]);
  const [selectedCases, setSelectedCases] = useState<string[]>([]);
  const [vus, setVus] = useState<string>("10");
  const [mode, setMode] = useState<"duration" | "iterations">("duration");
  const [durationSec, setDurationSec] = useState<string>("10");
  const [iterations, setIterations] = useState<string>("10");
  const [rampUpSec, setRampUpSec] = useState<string>("0");
  const [thinkMs, setThinkMs] = useState<string>("0");
  const [maxErrorRate, setMaxErrorRate] = useState<string>("");
  const [thresholds, setThresholds] = useState<ThresholdDraft[]>([
    { step: "", metric: "p95", op: "<", value: "500", unit: "ms" },
  ]);
  // Never persisted across runs: reset after each run and on project/env change.
  const [allowRemote, setAllowRemote] = useState<boolean>(false);
  const [backendTargets, setBackendTargets] = useState<LoadTargetReport | null>(null);
  const [fieldErrors, setFieldErrors] = useState<LoadProfileErrors>({});
  const [formError, setFormError] = useState<string>("");
  const [savePath, setSavePath] = useState<string>("load.json");
  const [savedNotice, setSavedNotice] = useState<string>("");
  const [saveError, setSaveError] = useState<string>("");
  const [savingReport, setSavingReport] = useState<boolean>(false);

  useEffect(() => {
    setAllowRemote(false);
  }, [info.path, environment]);

  const stepNames = useMemo(
    () => collectSelectedSteps(info.project, selectedSuites, selectedCases),
    [info.project, selectedSuites, selectedCases],
  );

  const localTargets = useMemo(
    () => collectProjectTargets(info.project, environment, selectedSuites, selectedCases, undefined, allowRemote),
    [info.project, environment, selectedSuites, selectedCases, allowRemote],
  );

  useEffect(() => {
    let active = true;
    void api
      .loadTargets({
        path: info.path,
        environment,
        suites: selectedSuites.length > 0 ? selectedSuites : undefined,
        cases: selectedCases.length > 0 ? selectedCases : undefined,
        vus: 1,
        iterations: 1,
        allowRemote,
      })
      .then((res) => {
        if (active) setBackendTargets(res);
      })
      .catch(() => {
        if (active) setBackendTargets(null);
      });
    return () => {
      active = false;
    };
  }, [info.path, environment, selectedSuites, selectedCases, allowRemote]);

  const targets: LoadTargetReport = backendTargets ?? localTargets;
  const refusedHosts = targets.refused ?? [];
  const hasRefused = refusedHosts.length > 0;
  const refusalText = hasRefused ? refusalMessage(refusedHosts) : "";

  const thresholdExprs = useMemo(
    () => thresholds.map(formatThreshold).filter(Boolean),
    [thresholds],
  );

  const profile = useMemo(() => validateLoadProfile({ vus, mode, durationSec, iterations, rampUpSec, thinkMs }),
    [vus, mode, durationSec, iterations, rampUpSec, thinkMs]);

  const currentOptions: LoadOptions | null = useMemo(() => {
    if (!profile.options) return null;
    return {
      path: info.path,
      environment,
      ...(selectedSuites.length > 0 ? { suites: selectedSuites } : {}),
      ...(selectedCases.length > 0 ? { cases: selectedCases } : {}),
      ...profile.options,
      ...(thresholdExprs.length > 0 ? { thresholds: thresholdExprs } : {}),
      ...(maxErrorRate.trim() ? { maxErrorRate: maxErrorRate.trim() } : {}),
      ...(allowRemote ? { allowRemote: true } : {}),
      ...(settings.timeoutMs ? { timeoutMs: settings.timeoutMs } : {}),
      ...(settings.noRedirects ? { noRedirects: true } : {}),
    };
  }, [
    info.path,
    environment,
    selectedSuites,
    selectedCases,
    profile,
    thresholdExprs,
    maxErrorRate,
    allowRemote,
    settings.timeoutMs,
    settings.noRedirects,
  ]);

  const command = useMemo(() => currentOptions ? loadCliCommand(info.path, currentOptions) : "", [info.path, currentOptions]);
  const highVuWarn = vuWarning(Number(vus));

  function toggleSuite(name: string) {
    setSelectedSuites((prev) => (prev.includes(name) ? prev.filter((s) => s !== name) : [...prev, name]));
  }

  function toggleCase(name: string) {
    setSelectedCases((prev) => (prev.includes(name) ? prev.filter((c) => c !== name) : [...prev, name]));
  }

  function updateThreshold(index: number, patch: Partial<ThresholdDraft>) {
    setThresholds((prev) => prev.map((row, i) => (i === index ? { ...row, ...patch } : row)));
  }

  async function handleStart() {
    onClearReport();
    setFormError("");
    setSavedNotice("");
    setSaveError("");
    setFieldErrors(profile.errors);
    if (!currentOptions) {
      setFormError("Correct the traffic profile fields below before starting.");
      return;
    }
    for (const row of thresholds) {
      if (row.value.trim() && !formatThreshold(row)) {
        setFormError(`Invalid threshold value "${row.value}". Enter a non-negative number.`);
        return;
      }
    }
    // Let core/load.ParseErrorRate validate the full Go float grammar and
    // return its diagnostic, including for scientific notation such as 1e-2.
    if (hasRefused && !allowRemote) {
      setFormError(refusalText);
      return;
    }

    const optsToRun = { ...currentOptions };
    // Never remember allowRemote between runs.
    setAllowRemote(false);
    try {
      await onStart(optsToRun);
    } catch (err) {
      setFormError(errorMessage(err));
    }
  }

  async function handleSaveReport() {
    if (!report) return;
    setSavingReport(true);
    setSaveError("");
    setSavedNotice("");
    try {
      const native = hasNativeDialogs();
      const out = native ? "" : savePath.trim() || "load.json";
      const written = await api.saveLoadReport({
        path: info.path,
        out,
        overwrite: true,
        report,
      });
      if (written) {
        setSavedNotice(`Saved to ${written}`);
      } else if (!native && typeof Blob !== "undefined" && typeof URL?.createObjectURL === "function") {
        const blob = new Blob([formatReportJson(report)], { type: "application/json" });
        const url = URL.createObjectURL(blob);
        const a = document.createElement("a");
        a.href = url;
        a.download = out;
        a.click();
        URL.revokeObjectURL(url);
        setSavedNotice(`Downloaded ${out}`);
      }
    } catch (err) {
      setSaveError(errorMessage(err));
    } finally {
      setSavingReport(false);
    }
  }

  const failureCount = report ? report.summary.assertionFailures + (report.summary.transportErrors ?? 0) + (report.summary.otherErrors ?? 0) : 0;
  const uncheckedFailures = !!report && !report.errorRateThreshold && failureCount > 0;
  const badgeStatus = running
    ? "running"
    : report?.interrupted
      ? "aborted"
      : report?.passed
        ? "passed"
        : "failed";
  const badgeLabel = running
    ? "Running"
    : report?.interrupted
      ? "INTERRUPTED"
      : report?.passed
        ? (uncheckedFailures ? "Thresholds passed" : "PASSED")
        : "FAILED";

  return (
    <div className="view-load" aria-busy={running}>
      <section className="runner-summary" aria-label="Load test configuration and status">
        <div className="runner-headline">
          {(running || report) && (
            <span className={cx("status-badge", `status-${badgeStatus}`)}>
              {!running && (
                <StatusMark
                  status={report?.interrupted ? "aborted" : report?.passed ? "passed" : "failed"}
                  decorative
                />
              )}
              {badgeLabel}
            </span>
          )}
          <h1 className="view-title">Load test · {info.project.name}</h1>
          {report && !running && (
            <span className="runner-meta">
              {[
                report.environment || environment || "default",
                `${report.vus} VUs`,
                `${report.iterations} iterations`,
                formatMs(report.durationMs),
                formatWhen(report.startedAt),
              ]
                .filter(Boolean)
                .join(" · ")}
            </span>
          )}
          {command && <LoadCliCommand command={command} />}
          {running ? (
            <button type="button" className="btn" onClick={onCancel}>
              <Stop size={13} />
              Cancel
            </button>
          ) : (
            <button
              type="button"
              className="btn btn-primary"
              disabled={hasRefused && !allowRemote}
              onClick={() => void handleStart()}
            >
              <Play size={13} />
              Start load test
            </button>
          )}
        </div>

        {!running && report && uncheckedFailures && (
          <p className="tone-warn" role="status">
            <Alert size={16} /> {failureCount} request failures/errors ({report.summary.assertionFailures} assertion failures, {(report.summary.transportErrors ?? 0) + (report.summary.otherErrors ?? 0)} errors). No max error rate was set.
          </p>
        )}

        {formError && (
          <div className="inline-error" role="alert">
            <Alert size={16} />
            <span>{formError}</span>
          </div>
        )}

        {running && (
          <div className="load-live-panel" role="status" aria-live="polite" aria-label="Live load statistics">
            <div className="stat-grid">
              <div className="stat-tile">
                <div className="stat-num mono">{live?.vus ?? vus}</div>
                <div className="stat-label">Active VUs</div>
              </div>
              <div className="stat-tile">
                <div className="stat-num mono">{(live?.rps ?? 0).toFixed(1)}</div>
                <div className="stat-label">Requests / sec (RPS)</div>
              </div>
              <div className="stat-tile">
                <div className="stat-num mono">{(live?.p95Ms ?? 0).toFixed(1)} ms</div>
                <div className="stat-label">p95 latency</div>
              </div>
              <div className="stat-tile">
                <div className={cx("stat-num", "mono", (live?.errors ?? 0) > 0 && "tone-fail")}>
                  {live?.errors ?? 0}
                </div>
                <div className="stat-label">Errors</div>
              </div>
            </div>
            <p className="mono muted">
              vus={live?.vus ?? vus} rps={(live?.rps ?? 0).toFixed(1)} p95={(live?.p95Ms ?? 0).toFixed(1)}ms errors=
              {live?.errors ?? 0}
              {live?.requests != null ? ` (${live.requests} requests)` : ""}
            </p>
          </div>
        )}
      </section>

      <div className="load-body">
        <section className="load-config" aria-label="Load test settings">
          <div className="load-card">
            <div className="load-card-head">
              <h2>Suites &amp; cases</h2>
              {(selectedSuites.length > 0 || selectedCases.length > 0) && (
                <button
                  type="button"
                  className="text-btn"
                  disabled={running}
                  onClick={() => {
                    setSelectedSuites([]);
                    setSelectedCases([]);
                  }}
                >
                  All suites &amp; cases
                </button>
              )}
            </div>
            <p className="muted">
              Each virtual user walks the selected cases in project order. Suite setup hooks run once first.
            </p>
            <div className="load-scope-list" role="group" aria-label="Select suites and cases">
              {(info.project.suites ?? []).map((suite) => {
                const suiteChecked = selectedSuites.includes(suite.name);
                return (
                  <div key={suite.name} className="load-suite-group">
                    <label className="check-row">
                      <input
                        type="checkbox"
                        disabled={running}
                        checked={suiteChecked}
                        onChange={() => toggleSuite(suite.name)}
                      />
                      <strong>{suite.name}</strong>
                    </label>
                    <div className="load-case-chips">
                      {suite.cases.map((tc) => {
                        const caseChecked = selectedCases.includes(tc.name);
                        return (
                          <label key={tc.name} className="check-row load-case-check">
                            <input
                              type="checkbox"
                              disabled={running}
                              checked={caseChecked}
                              onChange={() => toggleCase(tc.name)}
                            />
                            <span>{tc.name}</span>
                          </label>
                        );
                      })}
                    </div>
                  </div>
                );
              })}
            </div>
          </div>

          <div className="load-card">
            <h2>Traffic profile</h2>
            <div className="load-grid">
              <label className="field">
                <span>Virtual users (VUs)</span>
                <input
                  className="input mono"
                  type="text"
                  inputMode="decimal"
                  disabled={running}
                  aria-label="Virtual users"
                  aria-invalid={!!fieldErrors.vus}
                  aria-describedby={fieldErrors.vus ? "load-vus-error" : undefined}
                  value={vus}
                  onChange={(e) => setVus(e.target.value)}
                />
                {fieldErrors.vus && <span id="load-vus-error" className="tone-fail" role="alert">{fieldErrors.vus}</span>}
              </label>
              <label className="field">
                <span>Run mode</span>
                <select
                  className="input"
                  disabled={running}
                  aria-label="Run mode"
                  value={mode}
                  onChange={(e) => setMode(e.target.value as "duration" | "iterations")}
                >
                  <option value="duration">Duration</option>
                  <option value="iterations">Iterations per VU</option>
                </select>
              </label>
              {mode === "duration" ? (
                <label className="field">
                  <span>Duration (s)</span>
                  <input
                    className="input mono"
                    type="text"
                    inputMode="decimal"
                    disabled={running}
                    aria-label="Duration (s)"
                    aria-invalid={!!fieldErrors.durationSec}
                    aria-describedby={fieldErrors.durationSec ? "load-durationSec-error" : undefined}
                    value={durationSec}
                    onChange={(e) => setDurationSec(e.target.value)}
                  />
                  {fieldErrors.durationSec && <span id="load-durationSec-error" className="tone-fail" role="alert">{fieldErrors.durationSec}</span>}
                </label>
              ) : (
                <label className="field">
                  <span>Iterations per VU</span>
                  <input
                    className="input mono"
                    type="text"
                    inputMode="decimal"
                    disabled={running}
                    aria-label="Iterations per VU"
                    aria-invalid={!!fieldErrors.iterations}
                    aria-describedby={fieldErrors.iterations ? "load-iterations-error" : undefined}
                    value={iterations}
                    onChange={(e) => setIterations(e.target.value)}
                  />
                  {fieldErrors.iterations && <span id="load-iterations-error" className="tone-fail" role="alert">{fieldErrors.iterations}</span>}
                </label>
              )}
              <label className="field">
                <span>Ramp-up (s)</span>
                <input
                  className="input mono"
                  type="text"
                  inputMode="decimal"
                  disabled={running}
                  aria-label="Ramp-up (s)"
                  aria-invalid={!!fieldErrors.rampUpSec}
                  aria-describedby={fieldErrors.rampUpSec ? "load-rampUpSec-error" : undefined}
                  value={rampUpSec}
                  onChange={(e) => setRampUpSec(e.target.value)}
                />
                {fieldErrors.rampUpSec && <span id="load-rampUpSec-error" className="tone-fail" role="alert">{fieldErrors.rampUpSec}</span>}
              </label>
              <label className="field">
                <span>Think time (ms)</span>
                <input
                  className="input mono"
                  type="text"
                  inputMode="decimal"
                  disabled={running}
                  aria-label="Think time (ms)"
                  aria-invalid={!!fieldErrors.thinkMs}
                  aria-describedby={fieldErrors.thinkMs ? "load-thinkMs-error" : undefined}
                  value={thinkMs}
                  onChange={(e) => setThinkMs(e.target.value)}
                />
                {fieldErrors.thinkMs && <span id="load-thinkMs-error" className="tone-fail" role="alert">{fieldErrors.thinkMs}</span>}
              </label>
              <label className="field">
                <span>Max error rate</span>
                <input
                  className="input mono"
                  type="text"
                  placeholder="1% or 0.01"
                  disabled={running}
                  aria-label="Max error rate"
                  value={maxErrorRate}
                  onChange={(e) => setMaxErrorRate(e.target.value)}
                />
              </label>
            </div>
            <p className="muted">Duration, ramp-up, and think time support whole milliseconds, up to 24 hours each (86,400 s / 86,400,000 ms). Duration must be positive.</p>
            {highVuWarn && (
              <p className="tone-warn mono" role="status">
                {highVuWarn}
              </p>
            )}
          </div>

          <div className="load-card">
            <div className="load-card-head">
              <h2>Thresholds</h2>
              <button
                type="button"
                className="btn btn-sm"
                disabled={running}
                onClick={() =>
                  setThresholds((prev) => [
                    ...prev,
                    { step: "", metric: "p95", op: "<", value: "500", unit: "ms" },
                  ])
                }
              >
                <Plus size={13} />
                Add threshold
              </button>
            </div>
            {thresholds.length === 0 ? (
              <p className="muted">No latency thresholds set. Add one to fail the run when latency exceeds a bound.</p>
            ) : (
              <div className="load-threshold-list" role="group" aria-label="Latency thresholds">
                {thresholds.map((row, idx) => {
                  const expr = formatThreshold(row);
                  return (
                    <div key={idx} className="load-threshold-row">
                      <select
                        className="input"
                        disabled={running}
                        aria-label={`Threshold ${idx + 1} step`}
                        value={row.step}
                        onChange={(e) => updateThreshold(idx, { step: e.target.value })}
                      >
                        <option value="">Overall (all steps)</option>
                        {stepNames.map((name) => (
                          <option key={name} value={name}>
                            {name}
                          </option>
                        ))}
                      </select>
                      <select
                        className="input mono"
                        disabled={running}
                        aria-label={`Threshold ${idx + 1} metric`}
                        value={row.metric}
                        onChange={(e) => updateThreshold(idx, { metric: e.target.value as LoadMetric })}
                      >
                        {LOAD_METRICS.map((m) => (
                          <option key={m} value={m}>
                            {m}
                          </option>
                        ))}
                      </select>
                      <select
                        className="input mono"
                        disabled={running}
                        aria-label={`Threshold ${idx + 1} operator`}
                        value={row.op}
                        onChange={(e) => updateThreshold(idx, { op: e.target.value as LoadOp })}
                      >
                        {LOAD_OPS.map((op) => (
                          <option key={op} value={op}>
                            {op}
                          </option>
                        ))}
                      </select>
                      <input
                        className="input mono load-th-val"
                        type="number"
                        min={0}
                        step="any"
                        disabled={running}
                        aria-label={`Threshold ${idx + 1} limit`}
                        value={row.value}
                        onChange={(e) => updateThreshold(idx, { value: e.target.value })}
                      />
                      <select
                        className="input mono"
                        disabled={running}
                        aria-label={`Threshold ${idx + 1} unit`}
                        value={row.unit}
                        onChange={(e) => updateThreshold(idx, { unit: e.target.value as LoadUnit })}
                      >
                        {LOAD_UNITS.map((u) => (
                          <option key={u} value={u}>
                            {u}
                          </option>
                        ))}
                      </select>
                      {expr && <span className="snippet">{expr}</span>}
                      <button
                        type="button"
                        className="icon-btn"
                        disabled={running}
                        aria-label={`Remove threshold ${idx + 1}`}
                        onClick={() => setThresholds((prev) => prev.filter((_, i) => i !== idx))}
                      >
                        <Close size={14} />
                      </button>
                    </div>
                  );
                })}
              </div>
            )}
          </div>

          <div className="load-card">
            <div className="load-card-head">
              <Shield size={16} />
              <h2>Target hosts &amp; safety</h2>
            </div>
            <div className="load-target-chips" aria-label="Target hosts">
              {targets.hosts.length === 0 ? (
                <span className="muted">No target hosts in the selected steps.</span>
              ) : (
                targets.hosts.map((host) => {
                  const isRefused = refusedHosts.includes(host);
                  return (
                    <span
                      key={host}
                      className={cx("pill", "mono", isRefused ? "status-failed" : "status-passed")}
                    >
                      {host}
                      {isRefused ? " (non-private)" : " (private)"}
                    </span>
                  );
                })
              )}
            </div>
            {hasRefused && !allowRemote && (
              <div className="inline-error" role="alert">
                <Alert size={16} />
                <span className="mono">{refusalText}</span>
              </div>
            )}
            <div className="load-remote-box">
              <label className="check-row">
                <input
                  type="checkbox"
                  disabled={running}
                  checked={allowRemote}
                  onChange={(e) => setAllowRemote(e.target.checked)}
                />
                <strong>Allow remote targets (--allow-remote)</strong>
              </label>
              <p className="muted">
                Warning: Load testing sends concurrent traffic from all virtual users and disables the connect-time
                private-address guard. Only enable this for targets you own or have permission to test; this checkbox
                is never remembered between runs.
              </p>
            </div>
          </div>
        </section>

        <section className="load-results" aria-label="Load test results">
          {running || !report ? (
            <div className="runner-empty">
              <h2 className="view-title">{running ? "Load test in progress…" : "No load report yet"}</h2>
              <p>
                {running
                  ? "Live virtual users, throughput, 95th-percentile latency, and error counts update once a second above."
                  : "Configure virtual users, duration or iterations, and latency thresholds on the left, then start the load test."}
              </p>
            </div>
          ) : (
            <>
              <div className="load-card">
                <div className="load-card-head">
                  <h2>Per-step latency &amp; throughput</h2>
                  <div className="load-save-bar">
                    {!hasNativeDialogs() && (
                      <input
                        className="input mono save-input"
                        aria-label="Report file path"
                        value={savePath}
                        onChange={(e) => setSavePath(e.target.value)}
                        placeholder="load.json"
                      />
                    )}
                    <button
                      type="button"
                      className="btn btn-sm"
                      disabled={savingReport}
                      onClick={() => void handleSaveReport()}
                    >
                      <Save size={13} />
                      {savingReport ? "Saving…" : "Save JSON report"}
                    </button>
                  </div>
                </div>
                {savedNotice && (
                  <p className="tone-pass mono" role="status">
                    {savedNotice}
                  </p>
                )}
                {saveError && (
                  <p className="save-error" role="alert">
                    {saveError}
                  </p>
                )}
                <div className="load-table-scroll" tabIndex={0} role="region" aria-label="Scrollable per-step metrics">
                  <table className="env-table load-table" aria-label="Per-step latency and throughput">
                    <thead>
                      <tr>
                        <th scope="col">Step</th>
                        <th scope="col">Requests</th>
                        <th scope="col">Failures</th>
                        <th scope="col">Errors</th>
                        <th scope="col">RPS</th>
                        <th scope="col">Min</th>
                        <th scope="col">Mean</th>
                        <th scope="col">p50</th>
                        <th scope="col">p90</th>
                        <th scope="col">p95</th>
                        <th scope="col">p99</th>
                        <th scope="col">Max</th>
                      </tr>
                    </thead>
                    <tbody>
                      <MetricsRow row={{ ...report.summary, name: "overall" }} isOverall />
                      {report.steps.map((st) => (
                        <MetricsRow key={st.name || "step"} row={st} />
                      ))}
                    </tbody>
                  </table>
                </div>
              </div>

              <div className="load-card">
                <h2>Thresholds &amp; error rate</h2>
                {report.thresholds.length === 0 && !report.errorRateThreshold ? (
                  <p className="muted">No thresholds or max error rate were configured for this run.</p>
                ) : (
                  <div className="check-list">
                    {report.thresholds.map((th, i) => (
                      <div key={i} className={cx("check", !th.passed && "check-bad")}>
                        <div className="check-head">
                          <StatusMark status={th.passed ? "passed" : "failed"} />
                          <span className="mono">{th.expr}</span>
                          <span className="mono check-detail">
                            actual {th.actualMs.toFixed(2)} ms / limit {th.limitMs.toFixed(2)} ms
                          </span>
                        </div>
                        {!th.passed && th.message && <p className="mono muted">{th.message}</p>}
                      </div>
                    ))}
                    {report.errorRateThreshold && (
                      <div className={cx("check", !report.errorRateThreshold.passed && "check-bad")}>
                        <div className="check-head">
                          <StatusMark status={report.errorRateThreshold.passed ? "passed" : "failed"} />
                          <span className="mono">max-error-rate</span>
                          <span className="mono check-detail">
                            actual {(report.errorRateThreshold.actual * 100).toFixed(2)}% / max{" "}
                            {(report.errorRateThreshold.max * 100).toFixed(2)}%
                          </span>
                        </div>
                        {!report.errorRateThreshold.passed && report.errorRateThreshold.message && (
                          <p className="mono muted">{report.errorRateThreshold.message}</p>
                        )}
                      </div>
                    )}
                  </div>
                )}
              </div>

              {report.setup && report.setup.length > 0 && (
                <div className="load-card">
                  <h2>Suite setup hooks</h2>
                  <ul className="warn-list">
                    {report.setup.map((s, idx) => (
                      <li key={idx}>
                        <span className="mono">
                          {s.suite} / {s.name}
                        </span>
                        <span className={cx("pill", `status-${s.status}`)}>{s.status}</span>
                        {s.error && <span className="mono tone-fail">{s.error}</span>}
                      </li>
                    ))}
                  </ul>
                </div>
              )}

              {report.summary.errors && report.summary.errors.length > 0 && (
                <div className="load-card">
                  <h2>Recorded failure messages</h2>
                  <ul className="warn-list">
                    {report.summary.errors.map((errText, idx) => (
                      <li key={idx} className="mono tone-fail">
                        {errText}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </>
          )}
        </section>
      </div>
    </div>
  );
}

function MetricsRow({ row, isOverall }: { row: LoadMetrics; isOverall?: boolean }) {
  const errCount = (row.transportErrors ?? 0) + (row.otherErrors ?? 0);
  return (
    <tr className={cx(isOverall && "load-overall-row")}>
      <th scope="row" className="mono">
        {row.name || "overall"}
      </th>
      <td className="mono">{row.requests}</td>
      <td className={cx("mono", row.assertionFailures > 0 && "tone-fail")}>{row.assertionFailures}</td>
      <td className={cx("mono", errCount > 0 && "tone-fail")}>{errCount}</td>
      <td className="mono">{row.rps.toFixed(1)}</td>
      <td className="mono">{row.latency.minMs.toFixed(2)} ms</td>
      <td className="mono">{row.latency.meanMs.toFixed(2)} ms</td>
      <td className="mono">{row.latency.p50Ms.toFixed(2)} ms</td>
      <td className="mono">{row.latency.p90Ms.toFixed(2)} ms</td>
      <td className="mono">{row.latency.p95Ms.toFixed(2)} ms</td>
      <td className="mono">{row.latency.p99Ms.toFixed(2)} ms</td>
      <td className="mono">{row.latency.maxMs.toFixed(2)} ms</td>
    </tr>
  );
}

function LoadCliCommand({ command }: { command: string }) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  useEffect(() => {
    if (state === "idle") return;
    const timer = window.setTimeout(() => setState("idle"), 2000);
    return () => window.clearTimeout(timer);
  }, [state]);

  return (
    <span className="cli-chip mono">
      <span className="cli-text">{command}</span>
      <button
        type="button"
        className="cli-copy"
        aria-label={state === "copied" ? "Copied" : state === "failed" ? "Copy unavailable" : "Copy CLI command"}
        onClick={() => {
          void copyText(command).then((ok) => setState(ok ? "copied" : "failed"));
        }}
      >
        {state === "copied" ? <Check size={13} /> : <Copy size={13} />}
      </button>
      <span className="visually-hidden" aria-live="polite">
        {state === "copied" ? "Copied" : state === "failed" ? "Copy isn’t available in this browser" : ""}
      </span>
    </span>
  );
}
