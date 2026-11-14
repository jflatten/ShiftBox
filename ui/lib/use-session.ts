import { useQuery } from '@tanstack/react-query';
import type { ShellSession } from '../components/app-shell';
import { readProblem } from './api-problem';

interface SessionView extends Omit<ShellSession, 'role'> {
  user: { id: string; role: ShellSession['role']; mustChangePassword?: boolean };
  csrfToken: string;
  organizationSecretScopeId?: string;
  recoveryReviewId?: string;
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
  const session = !query.isError && query.data ? {
    ...query.data,
    role: query.data.user.role,
    mode: query.data.mode === 'normal' && query.data.user.mustChangePassword ? 'password_change' : query.data.mode,
  } : undefined;
  return { ...query, session };
}
