import { groupStepChanges, lineDiff, partLabel, reportedChangeCount } from "../lib/git";
import type { DiffLine } from "../lib/git";
import type { StepLocation } from "../lib/workspace";
import type { PartChange, ProjectChanges } from "../types";

export function SemanticChanges({ data, onOpen }: {
  data: ProjectChanges | null;
  onOpen?: (at: StepLocation) => void;
}) {
  const groups = groupStepChanges(data?.steps);
  const environments = data?.environments ?? [];
  const authChanges = data?.auth ?? [];
  const suites = data?.suites ?? [];
  const cases = data?.cases ?? [];
  const meta = data?.meta ?? [];

  return <>
    {data?.repo && reportedChangeCount(data) === 0 && <p className="empty">No changes since the last commit</p>}
    {data?.repo && meta.length > 0 && <section className="change-group">
      <h2>Project</h2>
      <PartTable parts={meta} />
    </section>}
    {data?.repo && (suites.length > 0 || cases.length > 0) && <ul className="scope-changes">
      {suites.map((item) => <li key={`suite-${item.state}-${item.name}`}>Suite {item.name} {item.state}{!!item.parts?.length && <PartTable parts={item.parts} />}</li>)}
      {cases.map((item) => <li key={`case-${item.state}-${item.suite}-${item.name}`}>Case {item.suite} / {item.name} {item.state}{!!item.parts?.length && <PartTable parts={item.parts} />}</li>)}
    </ul>}
    {groups.map((group) => <section key={group.suite} className="change-group">
      <h2>{group.suite}</h2>
      {group.cases.map((testCase) => <div key={testCase.name} className="change-case">
        <h3>{testCase.name}</h3>
        {testCase.steps.map((step) => <article key={`${step.state}-${step.index}-${step.name}`} className="change-request">
          <header>
            <strong>{step.name}</strong>
            <span className={`git-state git-state-${step.state}`}>{step.state}</span>
            {step.state === "moved" && <span className="muted">from {step.fromSuite} / {step.fromCase}</span>}
            {onOpen && step.state !== "removed" && <button className="btn btn-sm" onClick={() => onOpen({ suite: step.suite, caseName: step.case, index: step.index })}>Open request</button>}
          </header>
          {!!step.parts?.length && <PartTable parts={step.parts} />}
        </article>)}
      </div>)}
    </section>)}
    {environments.length > 0 && <section className="change-group">
      <h2>Environments</h2>
      {environments.map((env) => <article key={`${env.state}-${env.name}`} className="change-request">
        <header><strong>{env.name}</strong><span className={`git-state git-state-${env.state}`}>{env.state}</span></header>
        {!!env.parts?.length && <PartTable parts={env.parts} />}
      </article>)}
    </section>}
    {authChanges.length > 0 && <section className="change-group">
      <h2>Auth</h2>
      {authChanges.map((item) => <article key={`${item.state}-${item.name}`} className="change-request">
        <header><strong>{item.name}</strong><span className={`git-state git-state-${item.state}`}>{item.state}</span></header>
        {!!item.parts?.length && <PartTable parts={item.parts} />}
      </article>)}
    </section>}
    {!!data?.otherFiles && <p className="other-files">other changed files in this repo: {data.otherFiles}</p>}
  </>;
}

function PartTable({ parts }: { parts: PartChange[] }) {
  return <table className="change-table">
    <thead><tr><th>Part</th><th>Before</th><th /><th>After</th></tr></thead>
    <tbody>
      {parts.map((part, index) => part.part === "body" ? (
        <tr key={index}>
          <th>{partLabel(part)}</th>
          <td colSpan={3}>
            <div className="muted">changed</div>
            <LineDiff before={part.before} after={part.after} />
          </td>
        </tr>
      ) : (
        <tr key={index}>
          <th>{partLabel(part)}</th>
          <td className="mono">{part.before}</td>
          <td className="change-arrow" aria-hidden="true">→</td>
          <td className="mono">{part.after}</td>
        </tr>
      ))}
    </tbody>
  </table>;
}

function LineDiff({ before, after }: { before: string; after: string }) {
  return <pre className="line-diff">{lineDiff(before, after).map((line, index) => <DiffRow key={index} line={line} />)}</pre>;
}

function DiffRow({ line }: { line: DiffLine }) {
  const mark = line.kind === "add" ? "+" : line.kind === "del" ? "−" : " ";
  return <div className={`diff-line diff-${line.kind}`}>{mark} {line.text}</div>;
}
