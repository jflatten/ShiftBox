import type { components } from '../../lib/api-types';
import type { KeepAlivePolicy } from '../../lib/keep-alive';

export interface Workspace {
  id: string;
  name?: string;
  ownerId: string;
  templateId: string;
  templateRevision: number;
  platformRelease: string;
  providerKind?: string;
  desiredState: string;
  observedState: string;
  currentOperationId: string | null;
  lastFailedOperationId?: string | null;
  canOpenIde?: boolean;
  imageCache?: components['schemas']['WorkspaceImageCache'];
  probes?: components['schemas']['ProbeResult'][];
  previewPorts?: number[];
  canOpenPreviews?: boolean;
  canEditPreviewPorts?: boolean;
  keepAlivePolicy?: KeepAlivePolicy;
  keepAliveMinutes?: number;
  keepAliveOverrideMinutes?: number | null;
  inactivityShutdownAt?: string | null;
  inactivityMinutes?: number;
  canEditKeepAlive?: boolean;
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

export function workspaceIsTransitioning(workspace: Workspace) {
  return workspace.observedState !== workspace.desiredState &&
    (!!workspace.currentOperationId || ['provisioning', 'starting', 'stopping', 'deleting'].includes(workspace.observedState));
}

export function workspaceName(workspace: Workspace) {
  return workspace.name && workspace.name !== workspace.id ? workspace.name : 'Unnamed workspace';
}
