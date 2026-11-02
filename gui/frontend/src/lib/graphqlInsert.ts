import type { Draft } from "./workspace";
import type { GraphQLSchemaSource, GraphQLTemplate, Project, Step, Suite } from "../types";
import { formatJSONText, parseJSONText } from "./jsonText";

/** The schema source that applies to a step. A closer scope replaces the parent. */
export function effectiveGraphQLSchema(project?: Pick<Project, "schema"> | null, suite?: Pick<Suite, "schema"> | null, step?: Pick<Step, "schema"> | null): GraphQLSchemaSource | undefined {
  const chosen = step?.schema ?? suite?.schema ?? project?.schema;
  if (!chosen || (!chosen.file && !chosen.introspect)) return undefined;
  return chosen;
}

function parseObject(text: string): ReturnType<typeof parseJSONText> | undefined {
  try {
    const parsed = parseJSONText(text);
    if (parsed.kind === "object") return parsed;
  } catch { /* not an object */ }
  return undefined;
}

/** True when the variables editor has no keys. Invalid JSON counts as content. */
export function blankVariables(text: string): boolean {
  const trimmed = text.trim();
  if (trimmed === "" || trimmed === "{}") return true;
  const parsed = parseObject(trimmed);
  return parsed !== undefined && parsed.entries?.length === 0;
}

/**
 * Template variables, with values kept when the current object uses the same
 * name. Keys only the template declares are added. Other current keys are dropped.
 */
export function mergeVariableText(currentText: string, templateText: string): string {
  const template = parseObject(templateText);
  if (!template) return templateText.trim() ? templateText : "{}\n";
  const current = new Map((parseObject(currentText)?.entries ?? []).map(entry => [entry.key, entry.value.raw]));
  const entries = (template.entries ?? []).map(entry => `${entry.keyRaw}:${current.get(entry.key) ?? entry.value.raw}`);
  if (!entries.length) return "{}\n";
  return formatJSONText(`{${entries.join(",")}}`) + "\n";
}

/** Ask before replacing a query or variables editor that already has different text. */
export function needsReplaceConfirm(query: string, variablesText: string, template: GraphQLTemplate): boolean {
  const q = query.trim();
  if (q === "" && blankVariables(variablesText)) return false;
  const nextVars = mergeVariableText(variablesText, template.variables);
  return q !== template.query.trim() || variablesText.trim() !== nextVars.trim();
}

/** Writes a generated operation into the GraphQL draft. Matching variable values are kept. */
export function applyGraphQLTemplate(draft: Draft, template: GraphQLTemplate): Draft {
  const variablesText = mergeVariableText(draft.variablesText, template.variables);
  const step = { ...draft.step, request: { ...draft.step.request, query: template.query, operationName: template.operationName || undefined } };
  return { ...draft, step, variablesText };
}

/** Label shown in the operation picker. */
export function operationLabel(template: GraphQLTemplate): string {
  return template.operation || template.operationName;
}

/**
 * Small shop schema used by ?mock so the picker can be reviewed without the
 * Go backend. Required arguments are variables. Depth stops at scalars of the
 * selected field; friend is not expanded again.
 */
export const mockGraphQLTemplates: GraphQLTemplate[] = [
  {
    operation: "Query.widgets",
    kind: "query",
    operationName: "widgets",
    query: "query widgets($q: String!) {\n  widgets(q: $q) {\n    id\n    name\n  }\n}\n",
    variables: "{\n  \"q\": \"\"\n}\n",
  },
  {
    operation: "Query.widget",
    kind: "query",
    operationName: "widget",
    query: "query widget($id: ID!) {\n  widget(id: $id) {\n    id\n    name\n  }\n}\n",
    variables: "{\n  \"id\": \"\"\n}\n",
  },
];
