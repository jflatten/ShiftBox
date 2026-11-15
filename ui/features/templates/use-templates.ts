import { useQuery } from '@tanstack/react-query';
import { readProblem } from '../../lib/api-problem';

export interface TemplateRevision {
  id: string;
  name: string;
  ownerId: string;
  ownerName?: string;
  resourceProfileName?: string;
  platformImageName?: string;
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

export function useTemplates(cursor?: string, enabled = true, archived = false, userID?: string, admin = false, search = '') {
  return useQuery<{ items: TemplateRevision[]; nextCursor: string | null }>({
    queryKey: ['templates', userID ?? '', admin, archived, search, cursor ?? ''],
    enabled,
    queryFn: async ({ signal }) => {
      const params = new URLSearchParams();
      if (archived) params.set('archived', 'true');
      if (search) params.set('q', search);
      if (cursor) params.set('cursor', cursor);
      const suffix = params.size ? '?' + params : '';
      const response = await fetch('/api/v1/templates' + suffix, { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });
}
