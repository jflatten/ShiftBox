export interface Workspace {
  id: string;
  ownerId: string;
  templateId: string;
  templateRevision: number;
  desiredState: string;
  observedState: string;
  currentOperationId: string | null;
  canOpenIde?: boolean;
  version: number;
  resources: { cpuCount: number; memoryMiB: number; diskMiB: number };
}

export function workspaceState(value: string) {
  return value.charAt(0).toUpperCase() + value.slice(1);
}
