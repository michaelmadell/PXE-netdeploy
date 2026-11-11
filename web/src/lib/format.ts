const units = ["B", "KiB", "MiB", "GiB", "TiB"] as const;

/** 5368709120 → "5.0 GiB". Binary units, because the server's limits are. */
export function formatBytes(n: number | null | undefined): string {
  if (n == null || !Number.isFinite(n)) return "–";
  let v = n;
  let u = 0;
  while (v >= 1024 && u < units.length - 1) {
    v /= 1024;
    u++;
  }
  return `${u === 0 ? v : v.toFixed(v >= 100 ? 0 : 1)} ${units[u]}`;
}

const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return "–";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "–" : dateTime.format(d);
}

/** "3 min", "2 h 5 min": for time remaining. */
export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "–";
  if (seconds < 60) return `${Math.ceil(seconds)} s`;
  const m = Math.round(seconds / 60);
  if (m < 60) return `${m} min`;
  return `${Math.floor(m / 60)} h ${m % 60} min`;
}

/** First 12 characters of a hash, for tables; show the whole value in a title. */
export function shortHash(h: string | null | undefined): string {
  return h ? `${h.slice(0, 12)}…` : "–";
}
