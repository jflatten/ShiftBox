import { useQuery } from '@tanstack/react-query';
import type { ShellSession } from '../components/app-shell';
import { APIProblemError, ControllerUnavailableError, readControllerJSON } from './api-problem';
import { rememberSession, returnToLoginOnSessionLoss } from './login-navigation';
import { controllerPollInterval, controllerReadOptions } from './query-client';

interface SessionView extends Omit<ShellSession, 'role'> {
  user: { id: string; role: ShellSession['role']; mustChangePassword?: boolean;
    primaryEmail?: string; displayName?: string | null; credentialMode?: 'local' | 'external' };
  csrfToken: string;
  expiresAt: string;
  organizationSecretScopeId?: string;
  recoveryReviewId?: string;
}

/** Read the current management session without retaining stale authority after a failed refresh. */
export function useSession() {
  const query = useQuery<SessionView | null>({
    queryKey: ['session'],
    ...controllerReadOptions,
    refetchInterval: query => query.state.error instanceof ControllerUnavailableError ? controllerPollInterval : false,
    queryFn: async ({ signal }) => {
      try {
        const session = await readControllerJSON<SessionView>('/api/v1/session', { signal, cache: 'no-store' });
        rememberSession(session.expiresAt);
        return session;
      } catch (error) {
        if (error instanceof APIProblemError && error.problem.status === 401) {
          if (error.problem.code !== 'auth.required') returnToLoginOnSessionLoss();
          return null;
        }
        throw error;
      }
    },
  });
  const session = query.data ? {
    ...query.data,
    role: query.data.user.role,
    mode: query.data.mode === 'normal' && query.data.user.mustChangePassword ? 'password_change' : query.data.mode,
  } : undefined;
  return { ...query, session: query.isError ? undefined : session,
    cachedSession: query.error instanceof ControllerUnavailableError ? session : undefined };
}
