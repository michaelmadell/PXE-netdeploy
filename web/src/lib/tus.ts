/**
 * A small tus 1.0.0 client for the server's upload endpoint (ADR 0017):
 * creation, PATCH in chunks, HEAD to resume, DELETE to cancel.
 *
 * Uploads live in this module, not in a component, so they carry on while
 * the user moves between pages. The upload id is remembered per file in
 * localStorage, so choosing the same file again after a reload resumes it.
 */
import { useSyncExternalStore } from "react";
import { ApiError, type Problem } from "@/lib/problem";

const endpoint = "/api/v1/uploads";
const tusHeaders = { "Tus-Resumable": "1.0.0" };

/**
 * Bytes per PATCH. Each chunk is one request, so a dropped connection costs
 * at most this much progress display (the server keeps what it received).
 */
const chunkSize = 32 * 1024 * 1024;
const retryDelaysMs = [1_000, 3_000, 5_000, 10_000];

export type UploadStatus = "starting" | "uploading" | "paused" | "failed" | "complete";

export type UploadTask = {
  /** Local key, stable for the life of the page. */
  key: string;
  file: File;
  /** Server upload id, once created or found. */
  uploadId: string | null;
  status: UploadStatus;
  sentBytes: number;
  totalBytes: number;
  /** Recent rate, for the time remaining. */
  bytesPerSecond: number;
  /** True when this run continued an earlier upload of the same file. */
  resumed: boolean;
  error: string | null;
};

type Internal = UploadTask & {
  xhr: XMLHttpRequest | null;
  /** Bumped by pause/cancel so a running loop knows to stop. */
  run: number;
  onComplete?: (uploadId: string, key: string) => void;
  samples: { t: number; sent: number }[];
};

const tasks = new Map<string, Internal>();
const listeners = new Set<() => void>();
let snapshot: UploadTask[] = [];
let nextKey = 1;

function publish() {
  snapshot = [...tasks.values()].map(({ xhr: _x, run: _r, onComplete: _c, samples: _s, ...t }) => t);
  for (const l of listeners) l();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

/** Every upload started in this page, newest last. */
export function useUploads(): UploadTask[] {
  return useSyncExternalStore(subscribe, () => snapshot);
}

/** True while any upload is still sending; used to warn before leaving. */
export function uploadsActive(): boolean {
  return snapshot.some((t) => t.status === "starting" || t.status === "uploading");
}

const fingerprint = (f: File) => `netdeploy.upload:${f.name}:${f.size}:${f.lastModified}`;

function remember(f: File, uploadId: string | null) {
  try {
    if (uploadId) localStorage.setItem(fingerprint(f), uploadId);
    else localStorage.removeItem(fingerprint(f));
  } catch {
    // Private windows may refuse storage; resuming after a reload is then lost.
  }
}

function recall(f: File): string | null {
  try {
    return localStorage.getItem(fingerprint(f));
  } catch {
    return null;
  }
}

/** tus metadata: comma-separated `key base64(value)` pairs, UTF-8. */
function metadata(pairs: Record<string, string>): string {
  const enc = new TextEncoder();
  return Object.entries(pairs)
    .filter(([, v]) => v !== "")
    .map(([k, v]) => `${k} ${btoa(String.fromCharCode(...enc.encode(v)))}`)
    .join(",");
}

async function problemFrom(res: Response): Promise<ApiError> {
  let p: Partial<Problem> | null = null;
  try {
    p = (await res.json()) as Partial<Problem>;
  } catch {
    // HEAD answers and proxies have no problem body.
  }
  return new ApiError(res.status, p);
}

async function create(file: File): Promise<string> {
  const res = await fetch(endpoint, {
    method: "POST",
    headers: {
      ...tusHeaders,
      "Upload-Length": String(file.size),
      "Upload-Metadata": metadata({ filename: file.name, filetype: file.type }),
    },
  });
  if (res.status !== 201) throw await problemFrom(res);
  const id = res.headers.get("Location")?.split("/").pop();
  if (!id) throw new Error("The server created the upload but didn’t say where.");
  return id;
}

/** The server's offset, or null if the upload is gone (deleted or expired). */
async function offsetOf(uploadId: string): Promise<number | null> {
  const res = await fetch(`${endpoint}/${uploadId}`, { method: "HEAD", headers: tusHeaders, cache: "no-store" });
  if (res.status === 404) return null;
  if (!res.ok) throw await problemFrom(res);
  const offset = Number(res.headers.get("Upload-Offset"));
  if (!Number.isFinite(offset)) throw new Error("The server didn’t report how much of the upload it has.");
  return offset;
}

/** One PATCH. XMLHttpRequest, because fetch has no upload progress. */
function patch(t: Internal, offset: number, body: Blob): Promise<number> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    t.xhr = xhr;
    xhr.open("PATCH", `${endpoint}/${t.uploadId}`);
    xhr.setRequestHeader("Tus-Resumable", "1.0.0");
    xhr.setRequestHeader("Upload-Offset", String(offset));
    xhr.setRequestHeader("Content-Type", "application/offset+octet-stream");
    xhr.upload.onprogress = (e) => progress(t, offset + e.loaded);
    xhr.onload = () => {
      t.xhr = null;
      if (xhr.status === 204) {
        resolve(Number(xhr.getResponseHeader("Upload-Offset")));
        return;
      }
      let p: Partial<Problem> | null = null;
      try {
        p = JSON.parse(xhr.responseText) as Partial<Problem>;
      } catch {
        // No problem body.
      }
      reject(new ApiError(xhr.status, p));
    };
    xhr.onerror = () => {
      t.xhr = null;
      reject(new TypeError("network error"));
    };
    xhr.onabort = () => {
      t.xhr = null;
      reject(new DOMException("aborted", "AbortError"));
    };
    xhr.send(body);
  });
}

function progress(t: Internal, sent: number) {
  const now = performance.now();
  t.sentBytes = sent;
  t.samples.push({ t: now, sent });
  // Rate over the last ten seconds: steady enough to read, quick to follow a change.
  while (t.samples.length > 2 && now - t.samples[0]!.t > 10_000) t.samples.shift();
  const first = t.samples[0]!;
  const dt = (now - first.t) / 1000;
  t.bytesPerSecond = dt > 0.5 ? (sent - first.sent) / dt : 0;
  publish();
}

/** Errors worth another try: the network, or the server being briefly unavailable. */
function transient(e: unknown): boolean {
  if (e instanceof TypeError) return true;
  return e instanceof ApiError && (e.status === 409 || e.status === 423 || e.status >= 500) && e.status !== 507;
}

async function run(t: Internal) {
  const mine = ++t.run;
  const stopped = () => t.run !== mine;
  t.status = t.uploadId ? "uploading" : "starting";
  t.error = null;
  t.samples = [];
  publish();

  let failures = 0;
  for (;;) {
    try {
      // Find where to start: a remembered upload of this file, or a new one.
      let offset: number | null = null;
      if (!t.uploadId) {
        const known = recall(t.file);
        if (known) {
          offset = await offsetOf(known);
          if (offset !== null) {
            t.uploadId = known;
            t.resumed = offset > 0;
          }
        }
      } else {
        offset = await offsetOf(t.uploadId);
        if (offset === null) t.uploadId = null; // expired while paused: start again
      }
      if (stopped()) return;
      if (!t.uploadId) {
        t.uploadId = await create(t.file);
        remember(t.file, t.uploadId);
        offset = 0;
      }
      if (stopped()) return;
      t.status = "uploading";
      progress(t, offset ?? 0);

      let at = offset ?? 0;
      while (at < t.totalBytes) {
        at = await patch(t, at, t.file.slice(at, Math.min(at + chunkSize, t.totalBytes)));
        if (stopped()) return;
        failures = 0;
        progress(t, at);
      }

      t.status = "complete";
      t.sentBytes = t.totalBytes;
      remember(t.file, null);
      publish();
      t.onComplete?.(t.uploadId, t.key);
      return;
    } catch (e) {
      if (stopped() || (e instanceof DOMException && e.name === "AbortError")) return;
      const delay = retryDelaysMs[failures];
      if (transient(e) && delay !== undefined) {
        // The server keeps what it received; the next pass asks it where to resume.
        failures++;
        await new Promise((r) => setTimeout(r, delay));
        if (stopped()) return;
        continue;
      }
      t.status = "failed";
      t.error =
        e instanceof ApiError
          ? e.message
          : "The connection to the server was lost. What was sent is kept; try again to continue.";
      publish();
      return;
    }
  }
}

/** Starts (or resumes) uploading a file. `onComplete` gets the server's upload id. */
export function startUpload(file: File, onComplete?: (uploadId: string, key: string) => void): string {
  const key = String(nextKey++);
  const t: Internal = {
    key,
    file,
    uploadId: null,
    status: "starting",
    sentBytes: 0,
    totalBytes: file.size,
    bytesPerSecond: 0,
    resumed: false,
    error: null,
    xhr: null,
    run: 0,
    onComplete,
    samples: [],
  };
  tasks.set(key, t);
  void run(t);
  return key;
}

export function pauseUpload(key: string) {
  const t = tasks.get(key);
  if (!t || (t.status !== "uploading" && t.status !== "starting")) return;
  t.run++;
  t.xhr?.abort();
  t.status = "paused";
  t.bytesPerSecond = 0;
  publish();
}

/** Continues a paused or failed upload from wherever the server got to. */
export function resumeUpload(key: string) {
  const t = tasks.get(key);
  if (!t || (t.status !== "paused" && t.status !== "failed")) return;
  void run(t);
}

/** Marks a finished upload as failed (its import couldn't start); "Try again" re-runs `onComplete`. */
export function failUpload(key: string, message: string) {
  const t = tasks.get(key);
  if (!t) return;
  t.status = "failed";
  t.error = message;
  publish();
}

/** Stops an upload and removes it from the list. Deletes the server's copy too unless `keep`. */
export async function removeUpload(key: string, opts: { keep?: boolean } = {}) {
  const t = tasks.get(key);
  if (!t) return;
  t.run++;
  t.xhr?.abort();
  tasks.delete(key);
  publish();
  if (opts.keep || !t.uploadId) return;
  remember(t.file, null);
  try {
    await deleteUpload(t.uploadId);
  } catch {
    // It expires on its own; the uploads table still offers Delete.
  }
}

/** tus termination. Throws ApiError (409 `upload_in_use` while an import is using it). */
export async function deleteUpload(uploadId: string) {
  const res = await fetch(`${endpoint}/${uploadId}`, { method: "DELETE", headers: tusHeaders });
  if (!res.ok && res.status !== 404) throw await problemFrom(res);
}
