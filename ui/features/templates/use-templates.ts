import { useQuery } from '@tanstack/react-query';
import { readProblem } from '../../lib/api-problem';

export interface TemplateRevision {
  id: string;
  name: string;
  currentRevision: number;
  repositoryUrl: string;
  repositoryRef?: string;
  devcontainerPath: string;
  resourceProfileId: string;
  platformArtifactId: string;
  secretNames?: string[];
}

export function useTemplates(cursor?: string, enabled = true) {
  return useQuery<{ items: TemplateRevision[]; nextCursor: string | null }>({
    queryKey: ['templates', cursor ?? ''],
    enabled,
    queryFn: async ({ signal }) => {
      const suffix = cursor ? '?cursor=' + encodeURIComponent(cursor) : '';
      const response = await fetch('/api/v1/templates' + suffix, { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });
}
