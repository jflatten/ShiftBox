'use client';

import { useQuery } from '@tanstack/react-query';
import { readProblem } from '../../lib/api-problem';

export interface Notice {
  id: string;
  title: string;
  body: string;
  severity: 'info' | 'warning' | 'critical';
  effectiveAt?: string | null;
  readAt?: string | null;
  dismissedAt?: string | null;
  expiresAt?: string | null;
}

/** Toast and inbox share user-scoped receipts; polling also picks up scheduled notices. */
export function useNotices(userId: string, history = false, cursor?: string) {
  return useQuery<{ items: Notice[]; next?: string }>({
    queryKey: ['notices', userId, history, cursor],
    refetchInterval: 30_000,
    queryFn: async ({ signal }) => {
      const params = new URLSearchParams();
      if (history) params.set('includeDismissed', 'true');
      if (cursor) params.set('cursor', cursor);
      const response = await fetch('/api/v1/notices' + (params.size ? '?' + params : ''), { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      const link = response.headers.get('Link') ?? '';
      const next = /^<\/api\/v1\/notices\?([^<>]+)>; rel="next"$/.exec(link)?.[1];
      return { items: await response.json(), next: next ? new URLSearchParams(next).get('cursor') ?? undefined : undefined };
    },
  });
}
