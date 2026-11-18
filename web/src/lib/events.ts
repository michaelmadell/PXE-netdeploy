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
type Deployment = Schemas["Deployment"];
type DeploymentPage = Schemas["DeploymentPage"];

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
    case "driverPackage.updated":
      void qc.invalidateQueries({ queryKey: ["driver-packages"] });
      void qc.invalidateQueries({ queryKey: ["driver-package"] });
      break;
    case "job.updated":
      jobUpdated(qc, ev);
      break;
    case "client.updated":
      void qc.invalidateQueries({ queryKey: ["clients"] });
      void qc.invalidateQueries({ queryKey: ["client"] });
      break;
    case "deployment.updated":
      deploymentUpdated(qc, ev);
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
    void qc.invalidateQueries({ queryKey: ["driver-packages"] });
    void qc.invalidateQueries({ queryKey: ["driver-package"] });
    void qc.invalidateQueries({ queryKey: ["uploads"] });
    void qc.invalidateQueries({ queryKey: ["health"] });
    void qc.invalidateQueries({ queryKey: ["backups"] });
  }
}

/**
 * A running deployment reports each percent: patch the cached copies in
 * place. A new deployment or a status change re-reads lists, the deployment,
 * its client and its log.
 */
function deploymentUpdated(qc: QueryClient, ev: Event) {
  const d = (ev.data ?? {}) as Partial<Pick<Deployment, "status" | "stage" | "percent" | "clientId">>;
  const id = ev.resourceId;
  let unchanged = false;
  if (id && d.status) {
    const patch = (x: Deployment): Deployment => ({ ...x, stage: d.stage ?? x.stage, percent: d.percent ?? x.percent });
    const cur = qc.getQueryData<Deployment>(["deployment", id]);
    if (cur && cur.status === d.status) {
      qc.setQueryData<Deployment>(["deployment", id], patch(cur));
      unchanged = true;
    }
    let seen = false;
    qc.setQueriesData<DeploymentPage>({ queryKey: ["deployments"] }, (page) => {
      if (!page) return page;
      const i = page.items.findIndex((x) => x.id === id);
      if (i < 0 || page.items[i]!.status !== d.status) return page;
      seen = true;
      const items = page.items.slice();
      items[i] = patch(items[i]!);
      return { ...page, items };
    });
    unchanged = unchanged || seen;
  }
  if (!unchanged) {
    void qc.invalidateQueries({ queryKey: ["deployments"] });
    void qc.invalidateQueries({ queryKey: ["deployment"] });
    void qc.invalidateQueries({ queryKey: ["clients"] });
    void qc.invalidateQueries({ queryKey: ["client"] });
    if (id) void qc.invalidateQueries({ queryKey: ["deployment-logs", id] });
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
