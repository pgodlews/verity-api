import type { Status } from "../../types";

const GLYPH: Record<Status, string> = {
  passed: "✓",
  failed: "✕",
  error: "!",
  skipped: "–",
  aborted: "○",
};

const LABEL: Record<Status, string> = {
  passed: "Passed",
  failed: "Failed",
  error: "Error",
  skipped: "Skipped",
  aborted: "Aborted",
};

export function statusLabel(status: Status): string {
  return LABEL[status];
}

/** Status glyph. Shape and text travel with the colour. */
export function StatusMark({ status, decorative = false }: { status: Status; decorative?: boolean }) {
  const className = `status-glyph status-${status}`;
  if (decorative) {
    return (
      <span className={className} aria-hidden="true">
        {GLYPH[status]}
      </span>
    );
  }
  return (
    <span className={className} role="img" aria-label={LABEL[status]}>
      <span aria-hidden="true">{GLYPH[status]}</span>
    </span>
  );
}
