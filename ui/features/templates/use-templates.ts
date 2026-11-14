import { useQuery } from '@tanstack/react-query';
import { readProblem } from '../../lib/api-problem';

export interface TemplateRevision {
  id: string;
  name: string;
  ownerId: string;
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

export function useTemplates(cursor?: string, enabled = true, archived = false, userID?: string, admin = false) {
  return useQuery<{ items: TemplateRevision[]; nextCursor: string | null }>({
    queryKey: ['templates', userID ?? '', admin, archived, cursor ?? ''],
    enabled,
    queryFn: async ({ signal }) => {
      const params = new URLSearchParams();
      if (archived) params.set('archived', 'true');
      if (cursor) params.set('cursor', cursor);
      const suffix = params.size ? '?' + params : '';
      const response = await fetch('/api/v1/templates' + suffix, { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });
}
