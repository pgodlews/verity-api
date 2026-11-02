import { useEffect, useMemo, useRef, useState } from "react";
import { fileHashOf } from "../lib/git";
import type { AssertionResult, ProjectInfo, RunResult, Status, Step } from "../types";
import { Copy, Check, Play, Retry, Sliders, Stop } from "../icons";
import type { RunSettings } from "../lib/storage";
import { JsonBlock } from "./parts/JsonBlock";
import { StatusMark, statusLabel } from "./parts/StatusMark";
import {
  barWidth,
  capturedRequest,
  checkSummary,
  countSteps,
  defaultStepKey,
  failedCaseNames,
  flatten,
  methodPresentation,
  projectStep,
  resultSignature,
  rowLabel,
  stepNote,
  type FlatStep,
  type StepFilter,
} from "./parts/run";
import { cliCommand, copyText, cx, errorMessage, formatMs, formatValue, formatWhen } from "./parts/format";
import "./views.css";

/** secrets.Mask: the placeholder the core puts in place of a secret. */
const REDACTED = "***REDACTED***";

const FILTERS: { id: StepFilter; label: string }[] = [
  { id: "all", label: "All" },
  { id: "failed", label: "Failed" },
  { id: "error", label: "Errors" },
  { id: "passed", label: "Passed" },
  { id: "skipped", label: "Skipped" },
  { id: "aborted", label: "Aborted" },
];

const BAR_ORDER: Status[] = ["passed", "failed", "error", "skipped", "aborted"];

export function RunnerView(props: {
  info: ProjectInfo;
  result: RunResult | null;
  running: boolean;
  /** Steps the run expects to execute. Feeds can exceed this. */
  planned?: number;
  onCancel?: () => void;
  onRun: (opts: { suites?: string[]; cases?: string[] }) => void;
  onOpenStep: (suite: string, caseName: string, index: number) => void;
  onSaveStep: (suite: string, caseName: string, index: number, step: Step, baseline?: string, fileBaseline?: string) => Promise<string>;
  settings?: RunSettings;
  onSettings?: (settings: RunSettings) => void;
}): JSX.Element {
  const { info, result, running, planned = 0, onCancel, onRun, onOpenStep, onSaveStep, settings = {}, onSettings } = props;
  const options = onSettings && <RunOptions settings={settings} disabled={running} onChange={onSettings} />;
  const rows = useMemo(() => (result ? flatten(result) : []), [result]);
  const counts = useMemo(() => countSteps(rows), [rows]);
  const signature = resultSignature(result);
  const [filter, setFilter] = useState<StepFilter>("all");
  const [selected, setSelected] = useState<string | null>(() => defaultStepKey(result));

  const filterRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const wasRunning = useRef(false);

  useEffect(() => {
    if (running && !wasRunning.current) {
      setFilter("all");
      setSelected(null);
    }
    wasRunning.current = running;
    if (running) {
      setSelected((current) => current ?? defaultStepKey(result));
      return;
    }
    // A finished run replaces the streamed rows. Select its first failure.
    setFilter("all");
    setSelected(defaultStepKey(result));
  }, [running, signature, result]);

  if (!result) {
    return (
      <div className="view-runner">
        <div className="runner-empty">
          <h1 className="view-title">No results yet</h1>
          <p>Run the collection to see every request, check, and failure.</p>
          {running ? (
            <button type="button" className="btn" onClick={() => onCancel?.()}>
              <Stop size={13} />
              Cancel
            </button>
          ) : (
            <div className="runner-empty-actions">
              <button type="button" className="btn btn-primary" onClick={() => onRun({})}>
                <Play size={13} />
                Run collection
              </button>
              {options}
            </div>
          )}
        </div>
      </div>
    );
  }

  const visible = rows.filter((row) => filter === "all" || row.step.status === filter);
  const selectedRow = rows.find((row) => row.key === selected) ?? null;
  const maxMs = rows.reduce((max, row) => Math.max(max, row.step.durationMs), 0);
  const failedCases = failedCaseNames(result);
  const command = cliCommand(info.path, result.environment, settings);
  const done = rows.length;
  const progress = running
    ? planned > 0 && done <= planned
      ? `${done} of ${planned} steps`
      : `${done} ${done === 1 ? "step" : "steps"}`
    : "";
  const meta = [result.environment, running ? progress : formatMs(result.durationMs), running ? "" : formatWhen(result.startedAt)]
    .filter(Boolean)
    .join(" · ");
  const barLabel = BAR_ORDER.map((status) => `${counts[status]} ${statusLabel(status).toLowerCase()}`).join(", ");

  let previousSuite = "";
  let previousCase = "";

  return (
    <div className="view-runner" aria-busy={running}>
      <section className="runner-summary" aria-label="Run summary">
        <div className="runner-headline">
          {running ? (
            <span className="status-badge status-running">Running</span>
          ) : (
            <span className={cx("status-badge", `status-${result.summary.status}`)}>
              <StatusMark status={result.summary.status} decorative />
              {result.summary.status.toUpperCase()}
              {result.summary.aborted && result.summary.status !== "aborted" ? " · aborted" : ""}
            </span>
          )}
          <h1 className="view-title">{result.project}</h1>
          {meta && <span className="runner-meta">{meta}</span>}
          <CliCommand command={command} />
          {options}
          {running && (
            <button type="button" className="btn" onClick={() => onCancel?.()}>
              <Stop size={13} />
              Cancel
            </button>
          )}
          {failedCases.length > 0 && (
            <button
              type="button"
              className="btn btn-primary"
              disabled={running}
              onClick={() => onRun({ cases: failedCases })}
            >
              <Retry size={13} />
              {running ? "Running…" : "Re-run failed"}
            </button>
          )}
        </div>
        <div className="segbar" role="img" aria-label={barLabel}>
          {BAR_ORDER.filter((status) => counts[status] > 0).map((status) => (
            <span key={status} className={`seg seg-${status}`} style={{ flex: counts[status] }} />
          ))}
        </div>
        <div className="filter-row" role="radiogroup" aria-label="Filter steps">
          {FILTERS.map((item, index) => (
            <button
              key={item.id}
              ref={(node) => {
                filterRefs.current[index] = node;
              }}
              type="button"
              className="chip"
              role="radio"
              aria-checked={filter === item.id}
              tabIndex={filter === item.id ? 0 : -1}
              onClick={() => setFilter(item.id)}
              onKeyDown={(event) => {
                if (event.key !== "ArrowRight" && event.key !== "ArrowLeft") return;
                event.preventDefault();
                const direction = event.key === "ArrowRight" ? 1 : -1;
                const nextIndex = (index + direction + FILTERS.length) % FILTERS.length;
                setFilter(FILTERS[nextIndex].id);
                filterRefs.current[nextIndex]?.focus();
              }}
            >
              {item.id === "all" ? (
                <span className="dot-all" aria-hidden="true" />
              ) : (
                <span className={`dot status-${item.id}`} aria-hidden="true" />
              )}
              {item.label}
              <span className="chip-count">{counts[item.id]}</span>
            </button>
          ))}
        </div>
      </section>

      <div className="runner-body">
        <section className="runner-list" aria-label="Steps">
          {visible.length === 0 && (
            <p className="filter-empty">{running ? "Waiting for the first step…" : "Nothing matches this filter."}</p>
          )}
          {visible.map((row) => {
            const showSuite = row.suite !== previousSuite;
            const caseKey = `${row.suite}\0${row.caseIndex}`;
            const showCase = !!row.caseHeading && (showSuite || caseKey !== previousCase);
            previousSuite = row.suite;
            previousCase = caseKey;
            return (
              <div key={row.key}>
                {showSuite && <div className="label group-label">{row.suite}</div>}
                {showCase && <div className="case-head">{row.caseHeading}</div>}
                <StepButton info={info} row={row} selected={row.key === selected} maxMs={maxMs} onSelect={setSelected} />
              </div>
            );
          })}
        </section>
        <section className="runner-detail" aria-label="Step detail">
          {selectedRow ? (
            <StepDetail
              info={info}
              row={selectedRow}
              onOpenStep={onOpenStep}
              onSaveStep={onSaveStep}
            />
          ) : (
            <p className="empty">No steps in this run.</p>
          )}
        </section>
      </div>
    </div>
  );
}

// RunOptions sets the run-level timeout and redirect handling (the CLI's
// --timeout and --follow-redirects). A step's own timeoutMs still wins.
function RunOptions({ settings, disabled, onChange }: { settings: RunSettings; disabled: boolean; onChange: (settings: RunSettings) => void }) {
  const changed = !!settings.timeoutMs || !!settings.noRedirects;
  return (
    <details className="run-options">
      <summary className={cx("btn", changed && "run-options-changed")} title="Run options">
        <Sliders size={13} />
        Options{changed ? " ·" : ""}
      </summary>
      <div className="run-options-panel" role="group" aria-label="Run options">
        <label className="field">
          Timeout per request (ms)
          <input
            className="input mono"
            type="number"
            min={0}
            step={1000}
            placeholder="30000"
            disabled={disabled}
            value={settings.timeoutMs ?? ""}
            onChange={(event) => {
              const n = Math.floor(Number(event.target.value));
              onChange({ ...settings, timeoutMs: Number.isFinite(n) && n > 0 ? Math.min(n, 86_400_000) : undefined });
            }}
          />
          <span className="hint">For requests without their own timeout.</span>
        </label>
        <label className="check-row">
          <input
            type="checkbox"
            disabled={disabled}
            checked={!settings.noRedirects}
            onChange={(event) => onChange({ ...settings, noRedirects: event.target.checked ? undefined : true })}
          />
          Follow redirects
        </label>
      </div>
    </details>
  );
}

function CliCommand({ command }: { command: string }) {
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

function StepButton({
  info,
  row,
  selected,
  maxMs,
  onSelect,
}: {
  info: ProjectInfo;
  row: FlatStep;
  selected: boolean;
  maxMs: number;
  onSelect: (key: string) => void;
}) {
  // Steps that never ran (aborted, skipped) have no captured request; show
  // the method from the project instead of the protocol name.
  const method = methodPresentation(row.step.protocol, capturedRequest(row.step, projectStep(info.project, row.suite, row.caseName, row.index)));
  const note = stepNote(row.step);
  const time = row.step.status === "skipped" || row.step.status === "aborted" ? "—" : row.step.durationMs > 0 ? formatMs(row.step.durationMs) : "<1 ms";
  const attempts = row.step.attempts != null && row.step.attempts > 1 ? `${row.step.attempts} attempts` : "";
  return (
    <button
      type="button"
      className="step-row"
      aria-pressed={selected}
      onClick={() => onSelect(row.key)}
    >
      <StatusMark status={row.step.status} />
      <span className={cx("method", method.cls)}>{method.short}</span>
      <span className="step-text">
        <span className="step-name">{row.step.name}</span>
        <span className={cx("step-note", `note-${note.tone}`)}>{note.text}</span>
      </span>
      <span className="step-time">
        <span className="dur-track" aria-hidden="true">
          <span style={{ width: barWidth(row.step.durationMs, maxMs) }} />
        </span>
        <span className="mono step-ms">{attempts ? `${time} · ${attempts}` : time}</span>
      </span>
    </button>
  );
}

function StepDetail({
  info,
  row,
  onOpenStep,
  onSaveStep,
}: {
  info: ProjectInfo;
  row: FlatStep;
  onOpenStep: (suite: string, caseName: string, index: number) => void;
  onSaveStep: (suite: string, caseName: string, index: number, step: Step, baseline?: string, fileBaseline?: string) => Promise<string>;
}) {
  const { step } = row;
  const defined = projectStep(info.project, row.suite, row.caseName, row.index);
  const request = capturedRequest(step, defined);
  const method = methodPresentation(step.protocol, request);
  const assertions = step.assertions ?? [];
  const hasFailed = assertions.some((assertion) => !assertion.passed);
  const place = [row.suite, row.caseName !== row.suite ? row.caseName : "", rowLabel(row.iteration)]
    .filter(Boolean)
    .join(" · ");
  const requestLine = [method.verb, request?.url].filter(Boolean).join(" ");

  return (
    <>
      <div className="detail-head">
        <span className={cx("method", method.cls)}>{method.short}</span>
        <h2>{step.name}</h2>
        <span className="detail-suite">{place}</span>
        <span className={cx("pill", "push-end", `status-${step.status}`)}>
          <StatusMark status={step.status} decorative />
          {statusLabel(step.status)}
        </span>
        {!hasFailed && (
          <button type="button" className="btn btn-sm" onClick={() => onOpenStep(row.suite, row.caseName, row.index)}>
            Open request
          </button>
        )}
      </div>
      {requestLine && <p className="request-line mono">{requestLine}</p>}

      {assertions.length > 0 && (
        <div className="check-list">
          <div className="label">Checks · {checkSummary(assertions)}</div>
          {assertions.map((assertion, index) => (
            <CheckCard
              key={index}
              assertion={assertion}
              canAccept={!!defined?.assertions && index < defined.assertions.length}
              defined={defined}
              onOpen={() => onOpenStep(row.suite, row.caseName, row.index)}
              onAccept={async (actual) => {
                if (!defined?.assertions || index >= defined.assertions.length) return;
                const next: Step = JSON.parse(JSON.stringify(defined)) as Step;
                const value = actual !== null && typeof actual === "object" ? (JSON.parse(JSON.stringify(actual)) as unknown) : actual;
                next.assertions![index] = { ...next.assertions![index], value };
                const origin = info.stepFiles?.find((item) => item.suite === row.suite && item.case === row.caseName && item.index === row.index);
                const baseline = origin?.hash ?? "";
                await onSaveStep(row.suite, row.caseName, row.index, next, baseline, baseline ? undefined : fileHashOf(info, origin?.file || info.path));
              }}
            />
          ))}
        </div>
      )}

      {step.status === "error" && step.error && (
        <div className="error-box">
          <p className="error-box-title">{step.error.message || step.error.code}</p>
          <p className="mono error-box-code">
            {step.error.code}
            {step.error.path ? ` (${step.error.path})` : ""}
          </p>
        </div>
      )}

      {step.status === "skipped" && (
        <div className="skip-box">
          {defined?.skip
            ? "Skipped. This step is marked skip: true, so it was not sent."
            : "Skipped. This step was not sent."}
        </div>
      )}

      {step.status === "aborted" && (
        <div className="skip-box abort-box">Not started. The run was cancelled before this step was sent.</div>
      )}

      {step.response && (
        <div className="response-block">
          <div className="label response-label">
            Response
            <span className={cx("label-rest", httpTone(step.response.statusCode))}>
              {step.response.statusCode}
              {step.response.status ? ` ${step.response.status.replace(String(step.response.statusCode), "").trim()}` : ""}
            </span>
            <span className="label-soft">{formatMs(step.response.durationMs)}</span>
          </div>
          {step.response.body ? (
            <JsonBlock text={step.response.body} label="Response body" />
          ) : (
            <p className="muted">No response body.</p>
          )}
        </div>
      )}

      {step.captures && step.captures.length > 0 && (
        <div className="capture-block">
          <div className="label">Captures</div>
          <ul className="capture-list">
            {step.captures.map((capture) => (
              <li key={capture.name}>
                <span className="var-chip">{capture.name}</span>
                <span className="mono muted">{capture.value}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </>
  );
}

function CheckCard({
  assertion,
  canAccept,
  defined,
  onOpen,
  onAccept,
}: {
  assertion: AssertionResult;
  canAccept: boolean;
  defined: Step | undefined;
  onOpen: () => void;
  onAccept: (actual: unknown) => Promise<void>;
}) {
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [saveError, setSaveError] = useState("");
  const bad = !assertion.passed;
  // A masked actual value must never be written back as the expected value.
  const showAccept = bad && canAccept && assertion.actual !== undefined && !JSON.stringify(assertion.actual).includes(REDACTED);

  return (
    <div className={cx("check", bad && "check-bad")}>
      <div className="check-head">
        <StatusMark status={bad ? "failed" : "passed"} />
        <span>{assertion.description || assertion.type}</span>
        <span className="mono check-detail">{bad ? assertion.type : passedDetail(assertion)}</span>
      </div>
      {bad && (
        <>
          <div className="expect-grid">
            <div className="expect-cell">
              <div className="label">Expected</div>
              <div className="mono expect-value tone-pass">{formatValue(assertion.expected)}</div>
            </div>
            <div className="expect-cell">
              <div className="label">Actual</div>
              <div className="mono expect-value tone-fail">{formatValue(assertion.actual)}</div>
            </div>
          </div>
          <div className="check-actions">
            {showAccept && (
              <button
                type="button"
                className="btn btn-sm"
                disabled={saving || !defined}
                onClick={() => {
                  setSaveError("");
                  setSaving(true);
                  void onAccept(assertion.actual)
                    .then(() => setSaved(true))
                    .catch((err: unknown) => setSaveError(errorMessage(err)))
                    .finally(() => setSaving(false));
                }}
              >
                {saving ? "Saving…" : saved ? "Saved" : "Accept actual as expected"}
              </button>
            )}
            <button type="button" className="btn btn-sm" onClick={onOpen}>
              Open request
            </button>
            {saveError && (
              <span className="save-error" role="alert">
                {saveError}
              </span>
            )}
            {saved && !saveError && (
              <span className="visually-hidden" aria-live="polite">
                Saved expected value
              </span>
            )}
          </div>
        </>
      )}
    </div>
  );
}

function passedDetail(assertion: AssertionResult): string {
  if (assertion.message) return assertion.message;
  if (assertion.actual !== undefined) return formatValue(assertion.actual);
  if (assertion.expected !== undefined) return formatValue(assertion.expected);
  return assertion.type;
}

function httpTone(code: number): string {
  if (code >= 200 && code < 300) return "tone-pass";
  if (code >= 400) return "tone-fail";
  return "tone-muted";
}
