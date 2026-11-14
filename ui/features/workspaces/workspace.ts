export interface Workspace {
  id: string;
  ownerId: string;
  templateId: string;
  templateRevision: number;
  platformRelease: string;
  desiredState: string;
  observedState: string;
  currentOperationId: string | null;
  lastFailedOperationId?: string | null;
  canOpenIde?: boolean;
  canOpenRepair?: boolean;
  canStartRepair?: boolean;
  canRetryAfterRepair?: boolean;
  lastActivityAt: string | null;
  retentionDeadline: string | null;
  retention?: { state: 'disabled' | 'paused' | 'grace' | 'ready'; graceUntil?: string };
  maintenance?: {
    releaseId: string;
    deadline: string;
    state: 'pending' | 'waiting_safe' | 'updating' | 'verified' | 'restoring_state' | 'failed';
    operationId: string | null;
  };
  version: number;
  resources: { cpuCount: number; memoryMiB: number; diskMiB: number };
}

export function workspaceState(value: string) {
  return value.charAt(0).toUpperCase() + value.slice(1);
}
