// One subscriber for every Wails event the backend emits (run progress, load
// live stats, project changes, Git progress).

type WailsRuntime = {
  EventsOn?: (name: string, cb: (data: unknown) => void) => (() => void) | void;
  EventsOff?: (name: string) => void;
};

/**
 * onEvent subscribes handler to the Wails event name and returns the
 * unsubscribe function. Outside the Wails shell it uses fallback (the ?mock
 * bus) when one is given, and otherwise subscribes to nothing. The payload
 * is the backend's JSON for that event; T names its type.
 */
export function onEvent<T>(
  name: string,
  handler: (data: T) => void,
  fallback?: (handler: (data: T) => void) => () => void
): () => void {
  const runtime =
    typeof window !== "undefined" ? (window as unknown as { runtime?: WailsRuntime }).runtime : undefined;
  if (runtime?.EventsOn) {
    const off = runtime.EventsOn(name, (data) => handler(data as T));
    return () => {
      if (typeof off === "function") off();
      else runtime.EventsOff?.(name);
    };
  }
  return fallback ? fallback(handler) : () => {};
}
