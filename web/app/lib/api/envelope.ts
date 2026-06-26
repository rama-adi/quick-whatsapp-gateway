// Shared error + pagination envelopes (openapi §11).
// FROZEN — owned by the foundation agent. Surface agents import, never edit.

import type { components } from "./schema";

/** The closed set of error codes the API returns in {error:{code}}. */
export type ErrorCode =
  | "rate_limited"
  | "not_found"
  | "unauthorized"
  | "forbidden"
  | "validation_error"
  | "conflict"
  | "not_implemented"
  | "internal";

const ERROR_CODES: ReadonlySet<string> = new Set<ErrorCode>([
  "rate_limited",
  "not_found",
  "unauthorized",
  "forbidden",
  "validation_error",
  "conflict",
  "not_implemented",
  "internal",
]);

/** Narrow an unknown string to a known ErrorCode, defaulting to "internal". */
export function toErrorCode(code: unknown): ErrorCode {
  return typeof code === "string" && ERROR_CODES.has(code)
    ? (code as ErrorCode)
    : "internal";
}

/**
 * Thrown by fetchJSON for every non-2xx response. Carries the structured
 * {error:{code,message,details}} envelope plus the raw HTTP status.
 */
export class ApiError extends Error {
  readonly code: ErrorCode;
  readonly details?: Record<string, unknown>;
  readonly status?: number;

  constructor(
    code: ErrorCode,
    message: string,
    details?: Record<string, unknown>,
    status?: number,
  ) {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.details = details;
    this.status = status;
  }

  /** 501 endpoints (media download, channels, status-image, group :approve). */
  get isNotImplemented(): boolean {
    return this.code === "not_implemented";
  }

  get isUnauthorized(): boolean {
    return this.code === "unauthorized" || this.status === 401;
  }

  get isForbidden(): boolean {
    return this.code === "forbidden" || this.status === 403;
  }
}

export function isApiError(e: unknown): e is ApiError {
  return e instanceof ApiError;
}

/** The list envelope used by every paginated endpoint: {data, nextCursor}. */
export type Page<T> = {
  data: T[];
  nextCursor: string | null;
};

/** Raw error-envelope shape as it comes off the wire. */
type ErrorEnvelope = components["schemas"]["Error"];

/**
 * Parse a JSON error body into an ApiError. Tolerant of malformed bodies:
 * falls back to a generic internal error keyed off the HTTP status.
 */
export function parseError(body: unknown, status: number): ApiError {
  if (
    body &&
    typeof body === "object" &&
    "error" in body &&
    body.error &&
    typeof body.error === "object"
  ) {
    const env = body as ErrorEnvelope;
    const code = toErrorCode(env.error.code);
    const message = env.error.message || code;
    const details = env.error.details as Record<string, unknown> | undefined;
    return new ApiError(code, message, details, status);
  }
  // Map bare status codes when the body is not an envelope.
  const fallback: ErrorCode =
    status === 401
      ? "unauthorized"
      : status === 403
        ? "forbidden"
        : status === 404
          ? "not_found"
          : status === 409
            ? "conflict"
            : status === 429
              ? "rate_limited"
              : status === 501
                ? "not_implemented"
                : "internal";
  return new ApiError(fallback, `request failed with status ${status}`, undefined, status);
}
