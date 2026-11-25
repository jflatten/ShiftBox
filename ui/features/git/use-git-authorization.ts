import { useQuery } from '@tanstack/react-query';
import { readControllerJSON } from '../../lib/api-problem';
import type { components } from '../../lib/api-types';

export const gitAuthorizationEndpoint = '/api/v1/git/authorization';

export function useGitAuthorization(userID?: string) {
  return useQuery({
    queryKey: ['git-authorization', userID],
    enabled: !!userID,
    queryFn: ({ signal }) => readControllerJSON<components['schemas']['GitAuthorizationView']>(
      gitAuthorizationEndpoint, { cache: 'no-store', signal }),
    refetchInterval: query => query.state.data?.state === 'refreshing' ? 3000 : false,
  });
}
