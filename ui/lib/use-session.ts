import { useQuery } from '@tanstack/react-query';
import type { ShellSession } from '../components/app-shell';
import { readProblem } from './api-problem';

interface SessionView extends Omit<ShellSession, 'role'> {
  user: { id: string; role: ShellSession['role'] };
  csrfToken: string;
}

/** Read the current management session without retaining stale authority after a failed refresh. */
export function useSession() {
  const query = useQuery<SessionView | null>({
    queryKey: ['session'],
    queryFn: async ({ signal }) => {
      const response = await fetch('/api/v1/session', { signal, cache: 'no-store' });
      if (response.status === 401) return null;
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });
  const session = !query.isError && query.data ? { ...query.data, role: query.data.user.role } : undefined;
  return { ...query, session };
}
