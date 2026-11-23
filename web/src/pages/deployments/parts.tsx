import { useMutation, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, type Schemas } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Field, Select } from "@/components/ui/form";
import { Alert, Badge, Dialog, Progress } from "@/components/ui/misc";
import { errorMessage, unwrap } from "@/lib/problem";
import { useSettings } from "@/pages/boot/data";
import { clientName, useProfiles, type Client } from "@/pages/clients/data";
import {
  armClient,
  cancelDeployment,
  deploymentError,
  refreshDeployments,
  isLinux,
  stageLabel,
  statusText,
  statusTone,
  type Deployment,
} from "./data";

/** Status as a badge, with the stage and percent while it runs. */
export function DeploymentBadge({ d }: { d: Pick<Deployment, "status" | "stage" | "percent" | "profileSnapshot"> }) {
  const label = d.status === "running" ? `${d.stage ? stageLabel(d) : statusText.running} · ${d.percent ?? 0}%` : statusText[d.status];
  return <Badge tone={statusTone[d.status]}>{label}</Badge>;
}

/** Stage, bar and percent for a running deployment. */
export function DeploymentProgress({ d }: { d: Deployment }) {
  if (d.status !== "running") return null;
  const stage = stageLabel(d);
  return (
    <div className="min-w-40">
      <Progress value={d.percent ?? 0} label={`${stage}, ${d.percent ?? 0}%`} />
      <p className="mt-1 text-xs text-muted-foreground">
        {stage} · {d.percent ?? 0}%
      </p>
    </div>
  );
}

/** Confirm, then cancel. Says what happens to a running one. */
export function CancelDialog({ deployment, onClose }: { deployment: Deployment | null; onClose: () => void }) {
  const cancel = useMutation({
    mutationFn: (id: string) => cancelDeployment(id),
    onSuccess: () => {
      refreshDeployments();
      onClose();
    },
  });
  const running = deployment?.status === "running";
  return (
    <Dialog
      open={!!deployment}
      onClose={onClose}
      title={running ? "Stop this deployment?" : "Cancel this deployment?"}
      footer={
        <>
          <Button onClick={onClose}>Keep it</Button>
          <Button variant="destructive" disabled={cancel.isPending} onClick={() => deployment?.id && cancel.mutate(deployment.id)}>
            {cancel.isPending ? "Cancelling…" : running ? "Stop deployment" : "Cancel deployment"}
          </Button>
        </>
      }
    >
      {running && deployment && isLinux(deployment) ? (
        <div className="space-y-2 text-sm">
          <p>netdeploy stops tracking the install and ignores the installer’s reports from now on.</p>
          <p className="text-muted-foreground">
            The installer on the machine can’t be told to stop: it carries on unless you power the machine off, which can leave the disk
            partly installed. The deployment’s log is kept.
          </p>
        </div>
      ) : running ? (
        <div className="space-y-2 text-sm">
          <p>
            The machine finishes the step it is on (partitioning, applying the image or writing boot files), then stops and restarts.
            It learns of the cancel within a minute.
          </p>
          <p className="text-muted-foreground">
            Its disk may be left partitioned or partly installed and won’t start Windows until it is deployed again. The deployment’s log is kept.
          </p>
        </div>
      ) : (
        <p className="text-sm">The machine won’t be deployed on its next network boot; it gets the boot prompt instead. Nothing on it changes.</p>
      )}
      {cancel.isError && (
        <Alert tone="error" className="mt-4">
          {errorMessage(cancel.error)}
        </Alert>
      )}
    </Dialog>
  );
}

type Finding = Schemas["ValidationResult"]["findings"][number];

/** Findings of POST /profiles/{id}/validate, errors first. */
export function Findings({ findings }: { findings: Finding[] }) {
  if (findings.length === 0) return <Alert tone="success" title="No problems found" />;
  const order = { error: 0, warning: 1, info: 2 } as const;
  return (
    <ul className="space-y-2">
      {[...findings]
        .sort((a, b) => order[a.severity] - order[b.severity])
        .map((f, i) => (
          <li key={i}>
            <Alert tone={f.severity === "error" ? "error" : f.severity === "warning" ? "warning" : "info"}>{f.message}</Alert>
          </li>
        ))}
    </ul>
  );
}

export function useValidation(profileId: string | null) {
  return useQuery({
    queryKey: ["profile-validation", profileId],
    enabled: !!profileId,
    staleTime: 0,
    queryFn: async () =>
      unwrap(await api.POST("/profiles/{profileId}/validate", { params: { path: { profileId: profileId! } } })),
  });
}

/** Arm a client for its next network boot: choose the profile, see its problems first. */
export function ArmDialog({ client, onClose }: { client: Client | null; onClose: () => void }) {
  return (
    <Dialog open={!!client} onClose={onClose} title={client ? `Deploy ${clientName(client)}` : "Deploy"}>
      {client && <ArmForm key={client.id} client={client} onClose={onClose} />}
    </Dialog>
  );
}

function ArmForm({ client, onClose }: { client: Client; onClose: () => void }) {
  const profiles = useProfiles();
  const items = profiles.data?.items ?? [];
  const [profileId, setProfileId] = useState<string>(client.profileId ?? (items.length === 1 ? items[0]!.id : ""));
  const validation = useValidation(profileId || null);
  const [armed, setArmed] = useState<Deployment | null>(null);
  const arm = useMutation({
    mutationFn: () => armClient(client.id, profileId),
    onSuccess: (d) => {
      refreshDeployments();
      setArmed(d);
    },
  });
  const blocked = !validation.data?.ok;
  const expiry = useSettings().data?.deployments?.armedExpiryHours ?? 72;

  if (armed) {
    return (
      <div className="space-y-4">
        <Alert tone="success" title="Armed">
          The next time {clientName(client)} network boots it shows a short countdown, then installs without asking for the boot prompt password.
          {armed.computerName && (
            <>
              {" "}
              It will be named <span className="font-medium text-foreground">{armed.computerName}</span>.
            </>
          )}
        </Alert>
        <div className="flex justify-end">
          <Button variant="primary" onClick={onClose}>
            Done
          </Button>
        </div>
      </div>
    );
  }
  return (
    <div className="space-y-5">
      <Field
        label="Profile"
        hint={client.profileId && profileId !== client.profileId ? "Just for this deployment; the client keeps its own profile." : undefined}
      >
        {(p) => (
          <Select {...p} value={profileId} onChange={(e) => setProfileId(e.target.value)}>
            <option value="" disabled>
              Choose a profile…
            </option>
            {items.map((pr) => (
              <option key={pr.id} value={pr.id}>
                {pr.name}
                {pr.id === client.profileId ? " (the client’s)" : ""}
              </option>
            ))}
          </Select>
        )}
      </Field>
      {profileId &&
        (validation.isPending ? (
          <p className="text-sm text-muted-foreground">Checking the profile…</p>
        ) : validation.isError ? (
          <Alert tone="error" title="The profile couldn’t be checked">
            {errorMessage(validation.error)}
          </Alert>
        ) : (
          <Findings findings={validation.data.findings} />
        ))}
      <p className="text-sm text-muted-foreground">
        The machine installs on its next network boot, wiping the disk it chooses (the first internal disk of 32 GB or more). If it doesn’t
        network boot within {expiry} hours, the deployment expires.
      </p>
      {arm.isError && <Alert tone="error">{deploymentError(arm.error)}</Alert>}
      <div className="flex justify-end gap-2">
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="primary" disabled={!profileId || blocked || arm.isPending} onClick={() => arm.mutate()}>
          {arm.isPending ? "Arming…" : "Arm for next boot"}
        </Button>
      </div>
    </div>
  );
}
