// Rows for the request editor's Auth tab. Secret fields are masked unless the
// whole value is an ${env:…} or ${secret:…} reference.

export interface AuthConfigRow {
  key: string;
  text: string;
  masked: boolean;
}

const SECRET_KEYS = new Set([
  "password",
  "secret",
  "secretaccesskey",
  "sessiontoken",
  "token",
  "clientsecret",
  "refreshtoken",
  "keypem",
]);

export function isSecretAuthField(type: string, key: string): boolean {
  const norm = key.toLowerCase().replace(/[-_\s]/g, "");
  if (SECRET_KEYS.has(norm)) return true;
  return type === "apikey" && norm === "value";
}

function isOnlyReference(value: string): boolean {
  return /^\$\{(?:env|secret):[^}]+\}$/.test(value.trim());
}

function fieldOrder(type: string): string[] {
  switch (type) {
    case "aws_sigv4":
      return ["accessKeyId", "secretAccessKey", "sessionToken", "region", "service", "unsignedPayload"];
    case "hmac":
      return ["secret", "algorithm", "header", "encoding", "prefix", "message", "timestampHeader", "nonceHeader"];
    default:
      return [];
  }
}

export function authConfigRows(type: string, config: Record<string, unknown> | undefined): AuthConfigRow[] {
  const entries = Object.entries(config ?? {});
  const order = fieldOrder(type);
  entries.sort((a, b) => {
    const ia = order.indexOf(a[0]);
    const ib = order.indexOf(b[0]);
    if (ia === -1 && ib === -1) return a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0;
    if (ia === -1) return 1;
    if (ib === -1) return -1;
    return ia - ib;
  });
  return entries.map(([key, value]) => {
    const text = typeof value === "string" ? value : JSON.stringify(value);
    const masked = isSecretAuthField(type, key) && !isOnlyReference(text);
    return { key, text: masked ? "" : text, masked };
  });
}
