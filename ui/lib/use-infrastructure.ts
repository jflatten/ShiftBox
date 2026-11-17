import { useQuery } from '@tanstack/react-query';
import { readProblem } from './api-problem';

export type Capacity = { cpuCount: number; memoryMiB: number; diskMiB: number };
type Condition = { key: string; severity: 'info' | 'warning' | 'critical'; active: boolean; detail?: string };

export interface InfrastructureStatus {
  provider: { name: string; version: string; architecture: string; architectures?: string[]; networkModes: string[] | null };
  capacity: Capacity;
  version: number;
  conditions: Condition[];
}

const conditionTitles: Record<string, string> = {
  'browser_certificate.expiring': 'Browser certificate expires soon',
  'browser_certificate.invalid': 'Browser certificate needs attention',
};
const conditionColors = { info: 'blue', warning: 'yellow', critical: 'red' };

/** Share the current administrator's infrastructure status across management pages. */
export function useInfrastructure(userId?: string) {
  const query = useQuery<InfrastructureStatus>({
    queryKey: ['infrastructure', userId],
    enabled: !!userId,
    queryFn: async ({ signal }) => {
      const response = await fetch('/api/v1/admin/infrastructure', { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });
  const data = userId && !query.isError ? query.data : undefined;
  const alerts = (data?.conditions ?? []).filter(condition => condition.active).map(condition => ({
    id: condition.key, title: conditionTitles[condition.key] ?? 'Infrastructure condition',
    detail: condition.detail, color: conditionColors[condition.severity],
  }));
  return { ...query, data, alerts };
}

export function validLimit(value: string | number) {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0 ? null : 'Enter a positive whole number.';
}
