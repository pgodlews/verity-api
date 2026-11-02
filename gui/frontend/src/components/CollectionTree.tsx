import { useState } from "react";
import { ChevronDown, ChevronRight, Download, Filter, Folder } from "../icons";
import { stepMarker } from "../lib/git";
import { findResult, locationKey, method, methodClass } from "../lib/workspace";
import type { StepLocation } from "../lib/workspace";
import type { GitStepRef, ProjectInfo, RunResult } from "../types";

export function CollectionTree({ info, lastRun, selected, filter, onFilter, changedSteps, onOpen, onImport }: {
  info: ProjectInfo; lastRun: RunResult | null; selected: string | null;
  filter: string; onFilter: (filter: string) => void;
  changedSteps?: GitStepRef[];
  onOpen: (at: StepLocation) => void; onImport: () => void;
}) {
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const query = filter.trim().toLowerCase();
  const matches = (...text: string[]) => text.join(" ").toLowerCase().includes(query);
  const expanded = (key: string) => !!query || !collapsed.has(key);
  function toggle(key: string) { setCollapsed(prev => { const next = new Set(prev); if (next.has(key)) next.delete(key); else next.add(key); return next; }); }
  const suites = (info.project.suites ?? []).filter(s => s.cases.some(c => c.steps.some(st => matches(s.name, c.name, method(st), JSON.stringify(st), JSON.stringify({ ...s.vars, ...c.vars })))) || matches(s.name));
  return <aside className="collection-sidebar" aria-label="Collection">
    <div className="sidebar-heading"><span className="label">Collection</span><span className="count">{info.project.suites?.reduce((n, s) => n + s.cases.reduce((m, c) => m + c.steps.length, 0), 0) ?? 0}</span></div>
    <label className="tree-filter"><Filter size={14} /><input type="search" aria-label="Filter requests" placeholder="Filter" value={filter} onChange={e => onFilter(e.target.value)} /></label>
    <div className="collection-scroll">
      <div className="collection-name"><Folder size={14} /><strong>{info.project.name}</strong></div>
      {suites.map((suite, si) => {
        const suiteKey = JSON.stringify([suite.name]);
        return <div key={suiteKey} className="tree-suite">
          <button className="tree-button tree-group" aria-expanded={expanded(suiteKey)} onClick={() => toggle(suiteKey)}>{expanded(suiteKey) ? <ChevronDown size={12} /> : <ChevronRight size={12} />}<span className="tree-name">{suite.name}</span><small>suite</small></button>
          {expanded(suiteKey) && suite.cases.map((testCase, ci) => {
            const caseKey = JSON.stringify([suite.name, testCase.name]);
            const steps = testCase.steps.map((step, index) => ({ step, index })).filter(({ step }) => matches(suite.name, testCase.name, method(step), JSON.stringify(step), JSON.stringify({ ...suite.vars, ...testCase.vars })));
            if (query && !steps.length) return null;
            return <div key={`${si}-${ci}`}>
              <button className="tree-button tree-case" aria-expanded={expanded(caseKey)} onClick={() => toggle(caseKey)}>{expanded(caseKey) ? <ChevronDown size={12} /> : <ChevronRight size={12} />}<span className="tree-name">{testCase.name}</span><small>case</small></button>
              {expanded(caseKey) && steps.map(({ step, index }) => {
                const at = { suite: suite.name, caseName: testCase.name, index }, key = locationKey(at);
                const result = findResult(lastRun, at, step.name);
                const mark = stepMarker(changedSteps, suite.name, testCase.name, index, step.name);
                return <button key={index} className={`tree-button tree-step${step.skip ? " skipped-step" : ""}`} aria-current={selected === key ? "true" : undefined} title={step.name} onClick={() => onOpen(at)}>
                  <span className={methodClass(step)}>{method(step)}</span><span className="tree-name">{step.name}</span>{step.skip && <small>skip</small>}
                  {mark && <span className={`git-mark git-mark-${mark}`} title="Changed since the last commit">{mark}</span>}
                  <span className={`dot status-${result?.status ?? "none"}`} role="img" aria-label={result?.status ?? "not run"} />
                </button>;
              })}
            </div>;
          })}
        </div>;
      })}
      {!suites.length && <p className="empty">{query ? "No matching requests." : "This collection has no requests."}</p>}
    </div>
    <button className="btn sidebar-import" onClick={onImport}><Download size={14} />Import Postman / OpenAPI / WSDL</button>
  </aside>;
}
