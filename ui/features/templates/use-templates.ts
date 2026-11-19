import { useQuery } from '@tanstack/react-query';
import { readProblem } from '../../lib/api-problem';

export interface TemplateRevision {
  id: string;
  name: string;
  ownerId: string;
  ownerName?: string;
  resourceProfileName?: string;
  platformImageName?: string;
  providerKind?: string;
  state: 'active' | 'archived';
  version: number;
  currentRevision: number;
  repositoryUrl: string;
  repositoryRef?: string;
  devcontainerPath: string;
  resourceProfileId: string;
  platformArtifactId: string;
  networkPolicyId?: string;
  secretNames?: string[];
  launchGrants?: { userId: string; email: string }[];
}

interface TemplatePage {
  items: TemplateRevision[];
  nextCursor: string | null;
}

async function fetchTemplatePage(signal: AbortSignal, cursor?: string, archived = false, search = ''): Promise<TemplatePage> {
  const params = new URLSearchParams();
  if (archived) params.set('archived', 'true');
  if (search) params.set('q', search);
  if (cursor) params.set('cursor', cursor);
  const suffix = params.size ? '?' + params : '';
  const response = await fetch('/api/v1/templates' + suffix, { signal, cache: 'no-store' });
  if (!response.ok) throw await readProblem(response);
  return response.json();
}

export function useTemplates(cursor?: string, enabled = true, archived = false, userID?: string, admin = false, search = '') {
  return useQuery({
    queryKey: ['templates', userID ?? '', admin, archived, search, cursor ?? ''],
    enabled,
    queryFn: ({ signal }) => fetchTemplatePage(signal, cursor, archived, search),
  });
}

export function useLaunchProjects(userID: string, admin: boolean) {
  return useQuery({
    queryKey: ['templates', userID, admin, 'launch-catalog'],
    queryFn: async ({ signal }) => {
      // Both views filter the complete authorized catalog by name, independently of API page boundaries.
      const projects: TemplateRevision[] = [];
      let cursor: string | undefined;
      do {
        const { items, nextCursor } = await fetchTemplatePage(signal, cursor);
        projects.push(...items);
        cursor = nextCursor ?? undefined;
      } while (cursor);
      return projects.sort((a, b) => a.name.localeCompare(b.name));
    },
  });
}
