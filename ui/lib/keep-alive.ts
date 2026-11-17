export type KeepAlivePolicy = {
  maximumMinutes: number;
  allowedMinutes: number[];
  defaultMinutes: number;
  version: number;
};

// Keep duration arithmetic within the controller's time.Duration range.
const maximumDurationMinutes = 153_722_867;

/** Convert an administrator's duration notation to whole minutes; zero is Unlimited. */
export function parseKeepAlive(value: string): number | null {
  if (value.trim().toLowerCase() === 'unlimited') return 0;
  const match = /^(\d+)\s*(m|minutes?|h|hours?|d|days?)$/i.exec(value.trim());
  if (!match) return null;
  const unit = match[2].toLowerCase()[0];
  const minutes = Number(match[1]) * (unit === 'd' ? 1440 : unit === 'h' ? 60 : 1);
  return Number.isSafeInteger(minutes) && minutes > 0 && minutes <= maximumDurationMinutes ? minutes : null;
}

/** Parse distinct, comma-separated choices without accepting partial values. */
export function parseKeepAliveOptions(value: string): number[] | null {
  const parsed = value.split(',').map(parseKeepAlive);
  if (parsed.some(minutes => minutes === null) || new Set(parsed).size !== parsed.length) return null;
  return parsed as number[];
}

/** Display durations with readable units in settings and workspace controls. */
export function formatKeepAlive(minutes: number): string {
  if (minutes === 0) return 'Unlimited';
  const [amount, unit] = minutes % 1440 === 0 ? [minutes / 1440, 'day'] :
    minutes % 60 === 0 ? [minutes / 60, 'hour'] : [minutes, 'minute'];
  return `${amount} ${unit}${amount === 1 ? '' : 's'}`;
}

/** Use the short editable notation requested for administrator policy values. */
export function keepAliveNotation(minutes: number): string {
  if (minutes === 0) return 'Unlimited';
  return minutes % 1440 === 0 ? `${minutes / 1440}d` : minutes % 60 === 0 ? `${minutes / 60}h` : `${minutes}m`;
}
