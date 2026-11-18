import { useQuery } from '@tanstack/react-query';
import { readProblem } from './api-problem';
import type { components } from './api-types';

export type ImageCacheSettings = components['schemas']['ImageCacheSettings'];
export type ImageCacheSnapshot = components['schemas']['ImageCacheSnapshot'];

const endpoint = '/api/v1/admin/image-cache';

export function useImageCache(userId: string, active: boolean) {
  return useQuery<ImageCacheSnapshot>({
    queryKey: ['image-cache', userId],
    enabled: active,
    gcTime: 0,
    retry: false,
    refetchInterval: active ? query => {
      const phase = query.state.data?.activation.phase;
      return phase === 'staged' || phase === 'draining' ? 2000 : 30000;
    } : false,
    queryFn: async ({ signal }) => {
      const response = await fetch(endpoint, { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });
}

export async function saveImageCache(settings: ImageCacheSettings, version: number, csrfToken: string) {
  const response = await fetch(endpoint, {
    method: 'PUT', cache: 'no-store',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'If-Match': `"${version}"` },
    body: JSON.stringify(settings),
  });
  if (!response.ok) throw await readProblem(response);
  return response.status === 202 ? (await response.json() as { operationId: string }).operationId : undefined;
}
