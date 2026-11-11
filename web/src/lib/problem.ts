import type { Schemas } from "@/api/client";

export type Problem = Schemas["Problem"];
export type FieldError = { field: string; message: string };

/** An API call that failed with a problem document (or without one). */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: FieldError[];

  constructor(status: number, problem?: Partial<Problem> | null) {
    super(problem?.detail || problem?.title || `The server answered ${status}.`);
    this.name = "ApiError";
    this.status = status;
    this.code = problem?.code ?? "";
    this.fields = (problem?.errors ?? []).map((e) => ({ field: e.field ?? "", message: e.message ?? "" }));
  }
}

type Result<T> = { data?: T; error?: unknown; response: Response };

/** Unwraps an openapi-fetch result, throwing ApiError on a non-2xx answer. */
export function unwrap<T>(r: Result<T>): T {
  if (!r.response.ok) throw new ApiError(r.response.status, r.error as Partial<Problem> | undefined);
  return r.data as T;
}

/** A message a person can act on, whatever was thrown. */
export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof TypeError) return "Can’t reach the server. Check that netdeployd is running.";
  if (e instanceof Error) return e.message;
  return "Something went wrong.";
}
