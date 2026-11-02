import type { EnvRef } from "../../types";
import { envRefStatus } from "./format";

export function SecretChip({
  name,
  envRefs,
  withStatus = true,
}: {
  name: string;
  envRefs?: EnvRef[];
  withStatus?: boolean;
}) {
  const ref = withStatus ? envRefStatus(name, envRefs) : undefined;
  return (
    <span className="secret-ref">
      <span className="var-chip secret">{`\${env:${name}}`}</span>
      {ref && <span className={ref.set ? "secret-set" : "secret-unset"}>{ref.set ? "set" : "not set"}</span>}
    </span>
  );
}

/** Renders literal text, ${env:NAME} secret chips, and other ${…} chips. */
export function RichText({ text, envRefs }: { text: string; envRefs?: EnvRef[] }) {
  const parts = text.split(/(\$\{[^}]+\})/g);
  return (
    <>
      {parts.map((part, index) => {
        if (!part) return null;
        const env = part.match(/^\$\{env:([^}]+)\}$/);
        if (env) return <SecretChip key={index} name={env[1]} envRefs={envRefs} />;
        if (part.startsWith("${") && part.endsWith("}")) {
          return (
            <span key={index} className="var-chip">
              {part}
            </span>
          );
        }
        return <span key={index}>{part}</span>;
      })}
    </>
  );
}
