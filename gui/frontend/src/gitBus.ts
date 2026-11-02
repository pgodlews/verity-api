// Local bus for "project:changed". The desktop app uses Wails
// EventsOn("project:changed"); ?mock emits here so the disk-change banner
// can be reviewed in a browser. The dev bridge does not watch files.

import type { ProjectChanged } from "./types";

type Listener = (event: ProjectChanged) => void;

const listeners = new Set<Listener>();

export function subscribeProjectChanged(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function emitProjectChanged(event: ProjectChanged): void {
  for (const listener of listeners) listener(event);
}

type ProgressListener = (event: { op: string; line: string }) => void;

const progressListeners = new Set<ProgressListener>();

export function subscribeGitProgress(listener: ProgressListener): () => void {
  progressListeners.add(listener);
  return () => progressListeners.delete(listener);
}

export function emitGitProgress(event: { op: string; line: string }): void {
  for (const listener of progressListeners) listener(event);
}
