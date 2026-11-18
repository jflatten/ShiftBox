import { returnToLoginOnSessionLoss } from './login-navigation';

export interface APIProblem {
  type: string;
  title: string;
  status: number;
  code: string;
  detail?: string;
  requestId: string;
  retryable?: boolean;
  fields?: Record<string, string>;
}

/** Only the controller's public problem contract may supply displayed error text. */
export class APIProblemError extends Error {
  constructor(readonly problem: APIProblem) { super(problem.code); }
}

export class ControllerUnavailableError extends Error {
  constructor() { super('The ShiftBox controller is unavailable.'); }
}

/** Reads that drive live progress distinguish an unavailable controller from its domain errors. */
export async function readControllerJSON<T>(url: string, init: RequestInit): Promise<T> {
  let response: Response;
  try {
    response = await fetch(url, init);
  } catch {
    init.signal?.throwIfAborted();
    throw new ControllerUnavailableError();
  }
  if (!response.ok) {
    const error = await readProblem(response);
    init.signal?.throwIfAborted();
    if (error.problem.status >= 500 && ['internal.error', 'request.unavailable'].includes(error.problem.code)) {
      throw new ControllerUnavailableError();
    }
    throw error;
  }
  try {
    const data = await response.json();
    init.signal?.throwIfAborted();
    return data;
  } catch {
    init.signal?.throwIfAborted();
    throw new ControllerUnavailableError();
  }
}

export function fallbackProblem(status = 500): APIProblem {
  return { type: 'about:blank', title: 'Unable to complete the request', status, code: 'request.unavailable', detail: 'Try again. If the problem continues, contact your administrator.', requestId: '' };
}

/** Decode public problems; discard proxy pages, parser failures, and malformed objects. */
export async function readProblem(response: Response): Promise<APIProblemError> {
  const fallback = new APIProblemError(fallbackProblem(response.status));
  if (response.headers?.get('Content-Type')?.split(';')[0].trim().toLowerCase() !== 'application/problem+json') return fallback;
  try {
    const value: unknown = await response.json();
    if (!isProblem(value) || value.status !== response.status) return fallback;
    if (value.status === 401 && value.code === 'auth.required') returnToLoginOnSessionLoss();
    return new APIProblemError(value);
  } catch {
    return fallback;
  }
}

function isProblem(value: unknown): value is APIProblem {
  if (!value || typeof value !== 'object') return false;
  const p = value as Record<string, unknown>;
  return typeof p.type === 'string' && p.type.length > 0
    && typeof p.title === 'string' && p.title.length > 0
    && typeof p.status === 'number' && Number.isInteger(p.status) && p.status >= 400 && p.status <= 599
    && typeof p.code === 'string' && /^[a-z0-9_.-]+$/.test(p.code)
    && typeof p.requestId === 'string'
    && (p.detail === undefined || typeof p.detail === 'string')
    && (p.retryable === undefined || typeof p.retryable === 'boolean')
    && (p.fields === undefined || (p.fields !== null && typeof p.fields === 'object' && !Array.isArray(p.fields) && Object.values(p.fields).every(field => typeof field === 'string')));
}
