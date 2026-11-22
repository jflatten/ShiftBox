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

async function fetchTemplatePage(signal: AbortSignal, cursor: string | undefined, archived: boolean): Promise<TemplatePage> {
  const params = new URLSearchParams();
  if (archived) params.set('archived', 'true');
  if (cursor) params.set('cursor', cursor);
  const suffix = params.size ? '?' + params : '';
  const response = await fetch('/api/v1/templates' + suffix, { signal, cache: 'no-store' });
  if (!response.ok) throw await readProblem(response);
  return response.json();
}

export function useProjectCatalog(userID: string, admin: boolean, archived = false) {
  return useQuery({
    queryKey: ['templates', userID, admin, archived],
    queryFn: async ({ signal }) => {
      // Both layouts filter the entire authorized catalog, across API page boundaries.
      const projects: TemplateRevision[] = [];
      let cursor: string | undefined;
      do {
        const { items, nextCursor } = await fetchTemplatePage(signal, cursor, archived);
        projects.push(...items);
        cursor = nextCursor ?? undefined;
      } while (cursor);
      return projects.sort((a, b) => a.name.localeCompare(b.name));
    },
  });
}
