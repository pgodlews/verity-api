import type { StepLocation } from "../lib/workspace";
import { SourceControl } from "./SourceControl";

export function ChangesView({ path, epoch, dirty, active = true, onOpen, onChanged }: {
  path: string;
  epoch: number;
  dirty: boolean;
  active?: boolean;
  onOpen: (at: StepLocation) => void;
  onChanged: (worktree: boolean) => void;
}) {
  return <SourceControl path={path} epoch={epoch} dirty={dirty} active={active} onOpen={onOpen} onChanged={onChanged} />;
}
