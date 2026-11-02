import { useEffect, useState } from "react";
import { api } from "../api";
import { needsReplaceConfirm, operationLabel } from "../lib/graphqlInsert";
import type { GraphQLTemplate } from "../types";

/** Lists operations from the configured schema and inserts the chosen query. */
export function GraphQLInsert({ path, suite, caseName, index, environment, query, variablesText, undo, onInsert, onUndo }: {
  path: string;
  suite: string;
  caseName: string;
  index: number;
  environment: string;
  query: string;
  variablesText: string;
  undo: boolean;
  onInsert: (template: GraphQLTemplate) => void;
  onUndo: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [ops, setOps] = useState<GraphQLTemplate[] | null>(null);
  const [picked, setPicked] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [pending, setPending] = useState<GraphQLTemplate | null>(null);

  useEffect(() => { setOps(null); setOpen(false); setPending(null); }, [path, suite, caseName, index, environment]);

  async function toggle() {
    if (ops) {
      setOpen(value => !value);
      return;
    }
    setBusy(true);
    setError("");
    try {
      const list = await api.graphqlTemplates({ path, suite, case: caseName, index, environment });
      setOps(list);
      setPicked(list[0]?.operation ?? "");
      setOpen(true);
      if (!list.length) setError("The schema has no operations to insert.");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  function insert() {
    const template = ops?.find(op => op.operation === picked);
    if (!template) return;
    if (needsReplaceConfirm(query, variablesText, template)) {
      setPending(template);
      return;
    }
    setPending(null);
    onInsert(template);
  }

  function replace() {
    if (!pending) return;
    onInsert(pending);
    setPending(null);
  }

  return <div className="body-toolbar">
    <button type="button" className="btn btn-sm" disabled={busy} onClick={() => void toggle()}>{busy ? "Loading schema…" : "Insert from schema"}</button>
    {open && !!ops?.length && <>
      <select className="input" aria-label="Schema operation" value={picked} onChange={e => { setPicked(e.target.value); setPending(null); }}>
        {ops.map(op => <option key={op.operation} value={op.operation}>{operationLabel(op)}</option>)}
      </select>
      <button type="button" className="btn btn-sm" onClick={insert}>Insert</button>
    </>}
    {pending && <p className="insert-confirm">Replace the current query and variables?
      <button type="button" className="btn btn-sm btn-primary" onClick={replace}>Replace</button>
      <button type="button" className="btn btn-sm" onClick={() => setPending(null)}>Cancel</button>
    </p>}
    {undo && <button type="button" className="btn btn-sm" onClick={onUndo}>Undo insert</button>}
    {error && <p className="inline-error">{error}</p>}
  </div>;
}
