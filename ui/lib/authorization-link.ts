interface AuthorizationLink {
  href: string;
  expiresAt: number;
}

/** Check that the controller returned a fresh URL for the expected authorization endpoint. */
export function readAuthorizationLink(payload: unknown, origin: string, path: string): AuthorizationLink | undefined {
  if (!payload || typeof payload !== 'object') return;
  const { authorizationUrl, expiresAt } = payload as Record<string, unknown>;
  if (typeof authorizationUrl !== 'string' || typeof expiresAt !== 'string') return;
  const url = URL.parse(authorizationUrl);
  const deadline = Date.parse(expiresAt);
  if (!url || url.origin !== origin || url.pathname !== path || url.username || url.password || url.hash ||
    !url.searchParams.get('state') || !Number.isFinite(deadline) || deadline <= Date.now()) return;
  return { href: url.origin === window.location.origin ? url.pathname + url.search : url.href, expiresAt: deadline };
}
