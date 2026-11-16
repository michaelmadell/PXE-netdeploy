import { useQuery } from "@tanstack/react-query";
import { api, type Schemas } from "@/api/client";
import { queryClient } from "@/api/query";
import { ApiError, errorMessage, unwrap } from "@/lib/problem";

export type Deployment = Schemas["Deployment"];
export type DeploymentStatus = Schemas["DeploymentStatus"];
export type DeploymentStage = Schemas["DeploymentStage"];
export type LogEntry = Schemas["LogEntry"];

/** Query keys: ["deployments", filter] for lists, ["deployment", id], ["deployment-logs", id]. */
export type DeploymentFilter = { status?: DeploymentStatus; clientId?: string };

export function useDeployments(filter: DeploymentFilter = {}, opts: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: ["deployments", filter],
    enabled: opts.enabled ?? true,
    queryFn: async () =>
      unwrap(await api.GET("/deployments", { params: { query: { limit: 200, ...filter } } })),
  });
}

export function useDeployment(id: string) {
  return useQuery({
    queryKey: ["deployment", id],
    queryFn: async () => unwrap(await api.GET("/deployments/{deploymentId}", { params: { path: { deploymentId: id } } })),
  });
}

/** Every page of a deployment's log, oldest first. Re-read while it runs. */
export function useDeploymentLogs(id: string, running: boolean) {
  return useQuery({
    queryKey: ["deployment-logs", id],
    refetchInterval: running ? 5_000 : false,
    queryFn: async () => {
      const all: LogEntry[] = [];
      let cursor: string | undefined;
      do {
        const page = unwrap(
          await api.GET("/deployments/{deploymentId}/logs", {
            params: { path: { deploymentId: id }, query: { limit: 200, cursor } },
          }),
        );
        all.push(...page.items);
        cursor = page.nextCursor ?? undefined;
      } while (cursor);
      return all;
    },
  });
}

/** Pending and running deployments, by client id: what each client is doing now. */
export function useActiveDeployments() {
  const pending = useDeployments({ status: "pending" });
  const running = useDeployments({ status: "running" });
  const byClient = new Map<string, Deployment>();
  for (const d of [...(pending.data?.items ?? []), ...(running.data?.items ?? [])]) byClient.set(d.clientId, d);
  return byClient;
}

export function refreshDeployments() {
  for (const key of ["deployments", "deployment", "clients", "client"]) void queryClient.invalidateQueries({ queryKey: [key] });
}

export async function cancelDeployment(id: string) {
  return unwrap(await api.POST("/deployments/{deploymentId}/cancel", { params: { path: { deploymentId: id } } }));
}

export async function armClient(clientId: string, profileId: string) {
  return unwrap(await api.POST("/deployments", { body: { clientId, profileId } }));
}

export const isActive = (d: Pick<Deployment, "status">) => d.status === "pending" || d.status === "running";

export const stageText: Record<DeploymentStage, string> = {
  "checked-in": "Starting",
  partitioning: "Partitioning the disk",
  "applying-image": "Installing Windows",
  "injecting-drivers": "Adding drivers",
  configuring: "Configuring",
  finalizing: "Making the disk bootable",
  rebooting: "Restarting into Windows",
};

export const statusText: Record<DeploymentStatus, string> = {
  pending: "Armed",
  running: "Deploying",
  succeeded: "Installed",
  failed: "Failed",
  cancelled: "Cancelled",
};

export const statusTone: Record<DeploymentStatus, "neutral" | "success" | "warning" | "error"> = {
  pending: "warning",
  running: "warning",
  succeeded: "success",
  failed: "error",
  cancelled: "neutral",
};

/** The server's failure messages start with a reason (ADR 0026); say it plainly. */
export function outcome(d: Deployment): string | null {
  const m = d.message ?? "";
  if (d.status === "cancelled") return m.startsWith("superseded") ? "Replaced by a deployment started at the machine." : "Cancelled.";
  if (d.status !== "failed") return null;
  const reasons: [string, string][] = [
    ["restarted", "The machine restarted before the deployment finished."],
    ["timeout", "The machine stopped reporting progress, so the deployment was given up."],
    ["expired", "The machine didn’t network boot in time to pick up the deployment."],
    ["superseded", "Replaced by a deployment started at the machine."],
    ["plan", "The deployment couldn’t be prepared on the server."],
  ];
  for (const [prefix, text] of reasons) if (m.startsWith(prefix)) return `${text} (${m})`;
  return m || "The deployment failed.";
}

/** Seconds from start to finish (or now). */
export function elapsed(d: Deployment): number | null {
  if (!d.startedAt) return null;
  const end = d.finishedAt ? new Date(d.finishedAt).getTime() : Date.now();
  return Math.max(0, (end - new Date(d.startedAt).getTime()) / 1000);
}

const codes: Record<string, string> = {
  no_profile: "The client has no profile. Choose one.",
  deployment_active: "This client already has a deployment armed or running.",
  missing_odj_blob: "The profile joins the domain offline and this client has no ODJ blob. Choose one on the client.",
  invalid_computer_name: "The profile’s computer name pattern doesn’t give a valid name for this client.",
  profile_invalid: "The profile has problems that stop it being deployed.",
  missing_secret: "A secret the profile uses no longer exists.",
};
export const deploymentError = (e: unknown) => {
  if (e instanceof ApiError && codes[e.code]) return e.code === "invalid_computer_name" ? `${codes[e.code]} ${e.message}` : codes[e.code]!;
  return errorMessage(e);
};
