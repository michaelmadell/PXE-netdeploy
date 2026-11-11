import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { X } from "lucide-react";
import { api, type Schemas } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/misc";
import { errorMessage, unwrap } from "@/lib/problem";

type Job = Schemas["Job"];

/** Queued and running jobs of one kind, to show progress beside what they work on. */
export function useActiveJobs(kind: Schemas["JobKind"]) {
  return useQuery({
    queryKey: ["jobs", kind, "active"],
    queryFn: async () => {
      const get = async (status: "queued" | "running") =>
        unwrap(await api.GET("/jobs", { params: { query: { kind, status, limit: 200 } } })).items;
      const [running, queued] = await Promise.all([get("running"), get("queued")]);
      return { items: [...running, ...queued] };
    },
    // Events drive updates; this catches anything missed while the stream was down.
    refetchInterval: (q) => (q.state.data?.items.length ? 10_000 : false),
  });
}

/** A job's message, percentage, bar and Cancel. `what` names the work: "Import", "Build". */
export function JobProgress({ job, what, queuedText, compact }: { job: Job; what: string; queuedText: string; compact?: boolean }) {
  const qc = useQueryClient();
  const cancel = useMutation({
    mutationFn: async () => unwrap(await api.POST("/jobs/{jobId}/cancel", { params: { path: { jobId: job.id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["jobs"] }),
  });
  const label = job.status === "queued" ? queuedText : job.message || `${what} running`;
  return (
    <div>
      <div className="flex items-center justify-between gap-3 text-sm">
        <span className={compact ? "text-xs text-muted-foreground" : "font-medium"}>
          {job.cancelRequested ? "Cancelling…" : label}
        </span>
        <span className="flex items-center gap-2 text-xs text-muted-foreground tabular-nums">
          {job.status === "running" && `${job.percent}%`}
          {!job.cancelRequested && (
            <Button
              size={compact ? "icon" : "sm"}
              variant="ghost"
              disabled={cancel.isPending}
              onClick={() => cancel.mutate()}
              aria-label={`Cancel ${what.toLowerCase()}`}
            >
              <X aria-hidden /> {!compact && "Cancel"}
            </Button>
          )}
        </span>
      </div>
      <Progress className="mt-1.5" value={job.status === "running" ? job.percent : 0} label={`${what}: ${label}`} />
      {cancel.isError && <p className="mt-1 text-xs text-destructive">{errorMessage(cancel.error)}</p>}
    </div>
  );
}
