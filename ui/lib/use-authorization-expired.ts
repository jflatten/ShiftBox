import { useEffect, useState } from 'react';

/** Keep a mounted session or authorization link current at its server deadline. */
export function useAuthorizationExpired(deadline: number) {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    if (!Number.isFinite(deadline) || deadline <= now) return;
    const timer = window.setTimeout(() => setNow(Date.now()), Math.min(deadline - Date.now(), 2_147_483_647));
    return () => window.clearTimeout(timer);
  }, [deadline, now]);
  return Number.isFinite(deadline) && deadline <= now;
}
