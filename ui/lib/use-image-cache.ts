import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { readProblem } from './api-problem';
import type { components } from './api-types';

export type ImageCacheSettings = components['schemas']['ImageCacheSettings'];
export type ImageCacheSnapshot = components['schemas']['ImageCacheSnapshot'];
export type BuiltImagePolicy = components['schemas']['BuiltImagePolicy'];

const endpoint = '/api/v1/admin/image-cache';
const imageCacheKey = (userId: string) => ['image-cache', userId] as const;

export function useImageCache(userId: string, active: boolean) {
  return useQuery<ImageCacheSnapshot>({
    queryKey: imageCacheKey(userId),
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

export function useSaveBuiltImagePolicy(userId: string) {
  const client = useQueryClient();
  const queryKey = imageCacheKey(userId);
  return useMutation({
    networkMode: 'always',
    retry: false,
    mutationFn: async ({ enabled, version, csrfToken }: {
      enabled: boolean; version: number; csrfToken: string;
    }): Promise<BuiltImagePolicy> => {
      const response = await fetch(`${endpoint}/built-images`, {
        method: 'PUT', cache: 'no-store',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'If-Match': `"${version}"` },
        body: JSON.stringify({ enabled }),
      });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
    onSuccess: async saved => {
      // A poll started before the save must not overwrite the committed policy.
      await client.cancelQueries({ queryKey, exact: true });
      client.setQueryData<ImageCacheSnapshot>(queryKey, current => current && current.builtImages.version <= saved.version
        ? { ...current, builtImages: saved } : current);
      void client.invalidateQueries({ queryKey, exact: true });
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
