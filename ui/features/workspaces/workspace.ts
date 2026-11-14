export interface Workspace {
  id: string;
  ownerId: string;
  templateId: string;
  templateRevision: number;
  platformRelease: string;
  desiredState: string;
  observedState: string;
  currentOperationId: string | null;
  canOpenIde?: boolean;
  lastActivityAt: string | null;
  retentionDeadline: string | null;
  version: number;
  resources: { cpuCount: number; memoryMiB: number; diskMiB: number };
}

export function workspaceState(value: string) {
  return value.charAt(0).toUpperCase() + value.slice(1);
}
