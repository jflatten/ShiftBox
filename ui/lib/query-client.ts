import { QueryClient } from '@tanstack/react-query';

/** Each application owns an in-memory cache. Mutation entry points must reject offline submissions. */
export function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: { staleTime: 0, retry: false, networkMode: 'online', refetchOnWindowFocus: true },
      mutations: { retry: false, networkMode: 'online' },
    },
  });
}
