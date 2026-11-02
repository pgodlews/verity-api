import { useEffect, useRef, useState } from "react";
import { api, hasNativeDialogs } from "../api";
import type { ImportPreview } from "../types";
import { Alert, Braces, Check, Close, Code, File, Globe, Info, Layers, Lock, Shield } from "../icons";
import { JsonBlock } from "./parts/JsonBlock";
import { RichText } from "./parts/RichText";
import { baseName, cx, defaultYamlName, errorMessage, isExistsError, resolveOut } from "./parts/format";
import { groupWarnings, type NotedWarning, type SecretWarning } from "./parts/warnings";
import "./views.css";

type Kind = "postman" | "openapi" | "wsdl" | "soapui";

const SOURCES: { id: Kind; title: string; hint: string; icon: "layers" | "braces" | "code" | "file" }[] = [
  { id: "postman", title: "Postman collection", hint: "v2.0 / v2.1 JSON, plus environment files", icon: "layers" },
  { id: "openapi", title: "OpenAPI / Swagger", hint: "3.x or 2.0, YAML or JSON", icon: "braces" },
  { id: "wsdl", title: "WSDL", hint: "SOAP services, one case per operation", icon: "code" },
  { id: "soapui", title: "SoapUI project", hint: "SOAP and REST steps, assertions, property transfers", icon: "file" },
];

const PICK_TITLE: Record<Kind, string> = {
  postman: "Choose a Postman collection",
  openapi: "Choose an OpenAPI spec",
  wsdl: "Choose a WSDL file",
  soapui: "Choose a SoapUI project",
};

export function ImportView(props: { onImported: (projectPath: string) => void; onCancel: () => void }): JSX.Element {
  const { onImported, onCancel } = props;
  const native = hasNativeDialogs();
  const sourceRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const [kind, setKind] = useState<Kind>("postman");
  const [spec, setSpec] = useState("");
  const [envs, setEnvs] = useState<string[]>([]);
  const [saveAs, setSaveAs] = useState("project.yaml");
  const [saveTouched, setSaveTouched] = useState(false);
  const [preview, setPreview] = useState<ImportPreview | null>(null);
  const [showYaml, setShowYaml] = useState(false);
  const [error, setError] = useState("");
  const [exists, setExists] = useState(false);
  const [busy, setBusy] = useState<"preview" | "save" | "pick" | null>(null);

  useEffect(() => {
    if (!saveTouched) setSaveAs(spec ? defaultYamlName(spec) : "project.yaml");
  }, [spec, saveTouched]);

  function clearResult() {
    setPreview(null);
    setShowYaml(false);
    setExists(false);
    setError("");
  }

  function selectKind(next: Kind) {
    if (next === kind) return;
    setKind(next);
    setSpec("");
    setEnvs([]);
    setSaveTouched(false);
    clearResult();
  }

  function setSpecPath(path: string) {
    setSpec(path);
    clearResult();
  }

  async function browse(title: string): Promise<string> {
    setBusy("pick");
    setError("");
    try {
      return await api.pickFile(title);
    } catch (err) {
      setError(errorMessage(err));
      return "";
    } finally {
      setBusy(null);
    }
  }

  async function browseSpec() {
    const path = await browse(PICK_TITLE[kind]);
    if (path) setSpecPath(path);
  }

  async function addEnvironment() {
    const path = await browse("Choose a Postman environment");
    if (path && !envs.includes(path)) {
      setEnvs((current) => [...current, path]);
      clearResult();
    }
  }

  function takePaths(paths: string[]) {
    if (paths.length === 0) return;
    if (kind === "postman") {
      if (!spec) {
        setSpecPath(paths[0]);
        const rest = paths.slice(1).filter((path) => !envs.includes(path));
        if (rest.length) setEnvs((current) => [...current, ...rest]);
      } else {
        setEnvs((current) => [...current, ...paths.filter((path) => path !== spec && !current.includes(path))]);
        clearResult();
      }
      return;
    }
    setSpecPath(paths[0]);
  }

  function onDrop(event: React.DragEvent) {
    event.preventDefault();
    const files = Array.from(event.dataTransfer.files) as Array<File & { path?: string }>;
    const paths = files.map((file) => file.path).filter((path): path is string => !!path);
    if (files.length > 0 && paths.length === 0) {
      setError("Dropped files have no path. Browse or type the path instead.");
      return;
    }
    takePaths(paths);
  }

  async function runPreview() {
    if (!spec) return;
    setBusy("preview");
    setError("");
    setExists(false);
    try {
      const result = await api.importPreview(kind, spec, envPaths());
      setPreview(result);
      setShowYaml(false);
    } catch (err) {
      setPreview(null);
      setError(errorMessage(err));
    } finally {
      setBusy(null);
    }
  }

  async function save(overwrite: boolean) {
    const out = resolveOut(spec, saveAs);
    if (!spec || !out) return;
    setBusy("save");
    setError("");
    try {
      const saved = await api.importSave({
        kind,
        spec,
        envs: envPaths(),
        out,
        overwrite,
      });
      if (!saved) {
        setError("Import did not return a saved path.");
        return;
      }
      setExists(false);
      onImported(saved);
    } catch (err) {
      if (!overwrite && isExistsError(err)) setExists(true);
      else {
        setExists(false);
        setError(errorMessage(err));
      }
    } finally {
      setBusy(null);
    }
  }

  function envPaths(): string[] {
    return kind === "postman" ? envs.map((item) => item.trim()).filter(Boolean) : [];
  }

  const groups = preview ? groupWarnings(preview.warnings ?? []) : null;
  const outPath = resolveOut(spec, saveAs);
  const requestCount = preview?.stats.steps;

  return (
    <div className="view-import">
      <div className="import-body">
        <section className="import-source" aria-label="Source">
          <h1 className="view-title">Bring your APIs into Verity</h1>
          <p className="view-lead">
            Imports become plain YAML you can commit. Nothing is uploaded anywhere: conversion runs on this machine.
          </p>
          <div className="source-list" role="radiogroup" aria-label="Import from">
            {SOURCES.map((source, index) => (
              <button
                key={source.id}
                ref={(node) => {
                  sourceRefs.current[index] = node;
                }}
                type="button"
                className="src-option"
                role="radio"
                aria-checked={kind === source.id}
                tabIndex={kind === source.id ? 0 : -1}
                onClick={() => selectKind(source.id)}
                onKeyDown={(event) => {
                  if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
                  event.preventDefault();
                  const direction = event.key === "ArrowDown" ? 1 : -1;
                  const next = (index + direction + SOURCES.length) % SOURCES.length;
                  selectKind(SOURCES[next].id);
                  sourceRefs.current[next]?.focus();
                }}
              >
                <SourceIcon icon={source.icon} />
                <span className="src-copy">
                  <span className="src-title">{source.title}</span>
                  <span className="src-hint">{source.hint}</span>
                </span>
                {kind === source.id && (
                  <span className="src-check" aria-hidden="true">
                    <Check size={18} />
                  </span>
                )}
              </button>
            ))}
          </div>

          <div
            className="file-well"
            role="group"
            aria-label="Files"
            onDragOver={(event) => event.preventDefault()}
            onDrop={onDrop}
          >
            {native ? (
              spec ? (
                <FileRow
                  icon={kind === "wsdl" ? "code" : kind === "openapi" ? "braces" : "file"}
                  name={baseName(spec)}
                  detail={specDetail(kind, preview)}
                  onRemove={() => setSpecPath("")}
                />
              ) : (
                <button type="button" className="btn" disabled={busy === "pick"} onClick={() => void browseSpec()}>
                  Browse for a file
                </button>
              )
            ) : (
              <label className="field">
                <span>{specFieldLabel(kind)}</span>
                <input
                  className="input mono"
                  spellCheck={false}
                  value={spec}
                  aria-label={specFieldLabel(kind)}
                  onChange={(event) => setSpecPath(event.target.value)}
                />
              </label>
            )}

            {kind === "postman" &&
              native &&
              envs.map((env) => (
                <FileRow
                  key={env}
                  icon="globe"
                  name={baseName(env)}
                  detail="environment"
                  onRemove={() => {
                    setEnvs((current) => current.filter((item) => item !== env));
                    clearResult();
                  }}
                />
              ))}

            {kind === "postman" && !native && (
              <div className="env-paths">
                {envs.map((env, index) => (
                  <label className="field" key={index}>
                    <span>Environment file {index + 1}</span>
                    <span className="path-line">
                      <input
                        className="input mono"
                        spellCheck={false}
                        value={env}
                        aria-label={`Environment file ${index + 1}`}
                        onChange={(event) => {
                          const value = event.target.value;
                          setEnvs((current) => current.map((item, i) => (i === index ? value : item)));
                          clearResult();
                        }}
                      />
                      <button
                        type="button"
                        className="icon-btn"
                        aria-label={`Remove environment file ${index + 1}`}
                        onClick={() => {
                          setEnvs((current) => current.filter((_, i) => i !== index));
                          clearResult();
                        }}
                      >
                        <Close size={14} />
                      </button>
                    </span>
                  </label>
                ))}
              </div>
            )}

            {kind === "postman" && (
              <p className="file-hint">
                {native ? "Add environment files, or " : "Add environment paths, or "}
                <button
                  type="button"
                  className="text-btn"
                  disabled={native && busy === "pick"}
                  onClick={() => {
                    if (native) void addEnvironment();
                    else {
                      setEnvs((current) => [...current, ""]);
                      clearResult();
                    }
                  }}
                >
                  {native ? "browse" : "add another"}
                </button>
              </p>
            )}
            {native && spec && kind !== "postman" && (
              <p className="file-hint">
                <button type="button" className="text-btn" onClick={() => void browseSpec()}>
                  Choose a different file
                </button>
              </p>
            )}
          </div>
        </section>

        <section className="import-preview" aria-label="Preview">
          {!preview && !error && (
            <div className="card preview-empty">
              <p>Preview a file to see suites, checks, and anything the importer couldn’t convert.</p>
            </div>
          )}
          {error && (
            <div className="inline-error" role="alert">
              <Alert size={16} />
              <span>{error}</span>
            </div>
          )}
          {preview && (
            <>
              <div className="stat-grid">
                <Stat n={preview.stats.suites} label="suites" />
                <Stat n={preview.stats.steps} label="requests" />
                <Stat n={preview.stats.assertions} label="checks from tests" />
                <Stat n={preview.stats.auth} label="auth profiles" />
                <Stat n={preview.stats.cases} label="cases" />
                <Stat n={preview.stats.environments} label="environments" />
              </div>
              {groups && groups.secrets.length > 0 && (
                <WarningCard
                  icon={<Lock size={16} />}
                  iconClass="tone-warn"
                  title="Secrets to set"
                  subtitle="kept out of the project file"
                  count={groups.secrets.length}
                  countClass="count-warn"
                >
                  <div className="secret-table">
                    {groups.secrets.map((secret) => (
                      <SecretRow key={secret.raw} secret={secret} />
                    ))}
                  </div>
                </WarningCard>
              )}
              {groups && groups.notConverted.length > 0 && (
                <WarningCard
                  icon={<Braces size={16} />}
                  title="Not converted"
                  subtitle="Needs a manual look"
                  count={groups.notConverted.length}
                >
                  <WarningList items={groups.notConverted} />
                </WarningCard>
              )}
              {groups && groups.look.length > 0 && (
                <WarningCard
                  icon={<Info size={16} />}
                  iconClass="tone-accent"
                  title="Worth a look"
                  count={groups.look.length}
                >
                  <WarningList items={groups.look} />
                </WarningCard>
              )}
              {groups &&
                groups.secrets.length + groups.notConverted.length + groups.look.length === 0 && (
                  <p className="muted">No warnings. The importer converted everything it read.</p>
                )}
              {showYaml && preview.yaml && (
                <div className="yaml-block">
                  <div className="label">Preview YAML</div>
                  <JsonBlock text={preview.yaml} label="Imported project YAML" />
                </div>
              )}
            </>
          )}
        </section>
      </div>

      <footer className="import-footer">
        <span className="footer-note">
          <Shield size={15} />
          No secret values will be written to the project file.
        </span>
        <button type="button" className="btn" onClick={onCancel}>
          Cancel
        </button>
        <label className="save-as">
          Save as
          <input
            className="input mono save-input"
            spellCheck={false}
            value={saveAs}
            aria-label="Project file name"
            onChange={(event) => {
              setSaveTouched(true);
              setSaveAs(event.target.value);
              setExists(false);
            }}
          />
        </label>
        {outPath && spec && <span className="mono save-where">{outPath}</span>}
        <button type="button" className="btn" disabled={!spec || busy !== null} onClick={() => void runPreview()}>
          {busy === "preview" ? "Previewing…" : "Preview"}
        </button>
        {preview?.yaml && (
          <button type="button" className="btn" aria-pressed={showYaml} onClick={() => setShowYaml((value) => !value)}>
            Preview YAML
          </button>
        )}
        {exists ? (
          <span className="exists-offer" role="alert">
            This file already exists.
            <button type="button" className="btn btn-primary btn-sm" disabled={busy === "save"} onClick={() => void save(true)}>
              {busy === "save" ? "Saving…" : "Overwrite"}
            </button>
          </span>
        ) : (
          <button
            type="button"
            className="btn btn-primary"
            disabled={!spec || !saveAs.trim() || busy !== null}
            onClick={() => void save(false)}
          >
            {busy === "save" ? "Importing…" : requestCount === undefined ? "Import" : `Import ${requestCount} requests`}
          </button>
        )}
      </footer>
    </div>
  );
}

function SourceIcon({ icon }: { icon: "layers" | "braces" | "code" | "file" }) {
  const className = cx("src-icon", icon === "layers" && "src-post", icon === "braces" && "src-get", (icon === "code" || icon === "file") && "src-put");
  return (
    <span className={className} aria-hidden="true">
      {icon === "layers" && <Layers size={18} />}
      {icon === "braces" && <Braces size={18} />}
      {icon === "code" && <Code size={18} />}
      {icon === "file" && <File size={18} />}
    </span>
  );
}

function FileRow({
  icon,
  name,
  detail,
  onRemove,
}: {
  icon: "file" | "globe" | "code" | "braces";
  name: string;
  detail: string;
  onRemove: () => void;
}) {
  return (
    <div className="file-row">
      <span className="file-glyph" aria-hidden="true">
        {icon === "file" && <File size={16} />}
        {icon === "code" && <Code size={16} />}
        {icon === "braces" && <Braces size={16} />}
        {icon === "globe" && <Globe size={16} />}
      </span>
      <span className="file-copy">
        <span className="mono file-name">{name}</span>
        <span className="file-detail">{detail}</span>
      </span>
      <button type="button" className="icon-btn" aria-label={`Remove ${name}`} onClick={onRemove}>
        <Close size={14} />
      </button>
    </div>
  );
}

function specFieldLabel(kind: Kind): string {
  if (kind === "postman") return "Collection path";
  if (kind === "openapi") return "OpenAPI spec path";
  if (kind === "soapui") return "SoapUI project path";
  return "WSDL path";
}

function specDetail(kind: Kind, preview: ImportPreview | null): string {
  if (!preview) {
    if (kind === "postman") return "collection";
    if (kind === "openapi") return "OpenAPI spec";
    if (kind === "soapui") return "SoapUI project";
    return "WSDL";
  }
  const requests = `${preview.stats.steps} requests in ${preview.stats.suites} suites`;
  if (kind === "postman") return `collection · ${requests}`;
  if (kind === "openapi") return `OpenAPI spec · ${requests}`;
  if (kind === "soapui") return `SoapUI project · ${requests}`;
  return `WSDL · ${requests}`;
}

function Stat({ n, label }: { n: number; label: string }) {
  return (
    <div className="stat-tile">
      <div className="stat-num">{n}</div>
      <div className="stat-label">{label}</div>
    </div>
  );
}

function WarningCard({
  icon,
  iconClass,
  title,
  subtitle,
  count,
  countClass,
  children,
}: {
  icon: JSX.Element;
  iconClass?: string;
  title: string;
  subtitle?: string;
  count: number;
  countClass?: string;
  children: JSX.Element;
}) {
  return (
    <div className="warn-card">
      <div className="warn-head">
        <span className={iconClass} aria-hidden="true">
          {icon}
        </span>
        <h2>{title}</h2>
        {subtitle && <span className="warn-sub">{subtitle}</span>}
        <span className={cx("count", countClass)}>{count}</span>
      </div>
      {children}
    </div>
  );
}

function SecretRow({ secret }: { secret: SecretWarning }) {
  return (
    <div className="secret-row">
      <span className="mono secret-name">{secret.name}</span>
      <span className="secret-source">{secret.source}</span>
    </div>
  );
}

function WarningList({ items }: { items: NotedWarning[] }) {
  return (
    <ul className="warn-list">
      {items.map((item) => (
        <li key={item.raw}>
          {item.where && <span className="warn-where">{item.where}</span>}
          <span className="warn-detail">
            <RichText text={item.detail} />
          </span>
          {item.snippet && <code className="snippet">{item.snippet}</code>}
        </li>
      ))}
    </ul>
  );
}

