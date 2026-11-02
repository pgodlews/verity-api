import { useEffect, useState } from "react";
import { api } from "../api";
import type { OAuthRequest } from "../types";

export function OAuthSignIn(req: OAuthRequest) {
  const [signedIn, setSignedIn] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    setSignedIn(false);
    setError("");
    api.oauthStatus(req).then(value => { if (active) setSignedIn(value); })
      .catch(err => { if (active) setError(String(err.message ?? err)); });
    return () => { active = false; };
  }, [req.path, req.environment, req.authName]);

  async function toggle() {
    setBusy(true);
    setError("");
    try {
      if (signedIn) await api.oauthLogout(req);
      else await api.oauthLogin(req);
      setSignedIn(!signedIn);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }
  return <div>
    {signedIn && <span className="muted">Signed in · </span>}
    <button type="button" className="btn btn-sm" disabled={busy} onClick={toggle}>
      {busy ? "Waiting…" : signedIn ? "Sign out" : "Sign in"}
    </button>
    {error && <p className="inline-error" role="alert">{error}</p>}
  </div>;
}
