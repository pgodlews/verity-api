// Local progress bus for the browser mock. The desktop app uses Wails
// EventsOn("run:event") and EventsOn("load:live") instead; the dev bridge
// does not emit.

import type { LoadLiveStats, RunEvent } from "./types";

type Listener = (event: RunEvent) => void;
type LoadListener = (stats: LoadLiveStats) => void;

const listeners = new Set<Listener>();
const loadListeners = new Set<LoadListener>();

export function subscribeRunEvents(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function emitLocalRunEvent(event: RunEvent): void {
  for (const listener of listeners) listener(event);
}

export function subscribeLoadLive(listener: LoadListener): () => void {
  loadListeners.add(listener);
  return () => loadListeners.delete(listener);
}

export function emitLocalLoadLive(stats: LoadLiveStats): void {
  for (const listener of loadListeners) listener(stats);
}
