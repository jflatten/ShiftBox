import { QueryCache, QueryClient } from '@tanstack/react-query';
import { ControllerUnavailableError } from './api-problem';

export const controllerReadOptions = {
  meta: { controllerRead: true }, networkMode: 'always', refetchOnReconnect: true,
} as const;

export const controllerPollInterval = 3000;

/** Each application owns an in-memory cache. Mutation entry points must reject offline submissions. */
export function createQueryClient(onControllerUnavailable?: (unavailable: boolean) => void) {
  return new QueryClient({
    queryCache: new QueryCache({
      onSuccess: (_data, query) => {
        if (query.meta?.controllerRead) onControllerUnavailable?.(false);
      },
      onError: (error, query) => {
        if (query.meta?.controllerRead) onControllerUnavailable?.(error instanceof ControllerUnavailableError);
      },
    }),
    defaultOptions: {
      queries: { staleTime: 0, retry: false, networkMode: 'online', refetchOnWindowFocus: true },
      mutations: { retry: false, networkMode: 'online' },
    },
  });
}
