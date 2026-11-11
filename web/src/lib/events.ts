/**
 * One connection to GET /events for the whole app. Events are notifications
 * to re-read something (nothing is replayed), so each one invalidates the
 * queries it concerns, and a reconnect invalidates everything.
 */
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import type { Schemas } from "@/api/client";

type Event = Schemas["Event"];
type Job = Schemas["Job"];
type JobPage = Schemas["JobPage"];

const types: Event["type"][] = [
  "client.updated",
  "lease.updated",
  "settings.updated",
  "health.updated",
  "job.updated",
  "deployment.updated",
  "bootImage.updated",
  "image.updated",
  "driverPackage.updated",
  "profile.updated",
];

function handle(qc: QueryClient, ev: Event) {
  switch (ev.type) {
    case "health.updated":
      if (ev.data) qc.setQueryData(["health"], ev.data);
      else void qc.invalidateQueries({ queryKey: ["health"] });
      break;
    case "settings.updated":
      void qc.invalidateQueries({ queryKey: ["settings"] });
      break;
    case "image.updated":
      void qc.invalidateQueries({ queryKey: ["images"] });
      break;
    case "bootImage.updated":
      void qc.invalidateQueries({ queryKey: ["boot-images"] });
      break;
    case "profile.updated":
      void qc.invalidateQueries({ queryKey: ["profiles"] });
      break;
    case "job.updated":
      jobUpdated(qc, ev);
      break;
  }
}

/**
 * Progress arrives up to twice a second: patch the cached job instead of
 * re-reading the list each time. A status change (or a job we haven't seen)
 * re-reads jobs and whatever a finished import changed.
 */
function jobUpdated(qc: QueryClient, ev: Event) {
  const d = (ev.data ?? {}) as { status?: Job["status"]; percent?: number };
  let patched = false;
  if (ev.resourceId && d.status === "running") {
    qc.setQueriesData<JobPage>({ queryKey: ["jobs"] }, (page) => {
      if (!page) return page;
      const i = page.items.findIndex((j) => j.id === ev.resourceId && j.status === "running");
      if (i < 0) return page;
      patched = true;
      const items = page.items.slice();
      items[i] = { ...items[i]!, percent: d.percent ?? items[i]!.percent };
      return { ...page, items };
    });
  }
  if (!patched) {
    void qc.invalidateQueries({ queryKey: ["jobs"] });
    void qc.invalidateQueries({ queryKey: ["images"] });
    void qc.invalidateQueries({ queryKey: ["boot-images"] });
    void qc.invalidateQueries({ queryKey: ["uploads"] });
    void qc.invalidateQueries({ queryKey: ["health"] });
  }
}

/** Mount once, near the root. */
export function useServerEvents() {
  const qc = useQueryClient();
  useEffect(() => {
    const es = new EventSource("/api/v1/events");
    let dropped = false;
    es.onerror = () => {
      // EventSource reconnects by itself; note that events may have been missed.
      dropped = true;
    };
    es.onopen = () => {
      if (dropped) void qc.invalidateQueries();
      dropped = false;
    };
    const listener = (m: MessageEvent<string>) => {
      try {
        handle(qc, JSON.parse(m.data) as Event);
      } catch {
        // A malformed event is not worth breaking the stream for.
      }
    };
    for (const t of types) es.addEventListener(t, listener);
    return () => es.close();
  }, [qc]);
}
