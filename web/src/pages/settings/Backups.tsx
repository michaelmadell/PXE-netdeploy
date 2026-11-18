import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { HardDriveDownload } from "lucide-react";
import { Link } from "react-router";
import { api, type Schemas } from "@/api/client";
import { JobProgress, useActiveJobs } from "@/components/JobProgress";
import { Button } from "@/components/ui/button";
import { Checkbox, Field, Input, NumberInput, Select } from "@/components/ui/form";
import { Alert, Badge, Section, td, th } from "@/components/ui/misc";
import { formatBytes, formatDateTime, shortHash } from "@/lib/format";
import { errorMessage, unwrap } from "@/lib/problem";
import { useSecrets } from "@/pages/clients/data";

type Backup = Schemas["BackupSettings"];
type Destination = NonNullable<Backup["destination"]>;
type Frequency = NonNullable<NonNullable<Backup["schedule"]>["frequency"]>;
type Day = NonNullable<NonNullable<Backup["schedule"]>["daysOfWeek"]>[number];

const days: [Day, string][] = [
  ["mon", "Mon"],
  ["tue", "Tue"],
  ["wed", "Wed"],
  ["thu", "Thu"],
  ["fri", "Fri"],
  ["sat", "Sat"],
  ["sun", "Sun"],
];

type Schedule = NonNullable<Backup["schedule"]>;
const defaultSchedule: Schedule = { frequency: "disabled", minute: 0, time: "02:00", daysOfWeek: ["sun"], dayOfMonth: 1 };

const triggerText: Record<Schemas["Backup"]["trigger"], string> = {
  scheduled: "Scheduled",
  manual: "Back up now",
  offline: "Server stopped (netdeployd backup)",
};

/** The `backup` settings: where, when, how many. Part of the settings draft. */
export function BackupSettingsSection({ backup, edit }: { backup: Backup; edit: (fn: (b: Backup) => void) => void }) {
  const shares = useSecrets("share-credential");
  const dest = backup.destination ?? null;
  const schedule = backup.schedule ?? defaultSchedule;
  const frequency: Frequency = schedule.frequency;
  const editSchedule = (fn: (s: Schedule) => void) =>
    edit((b) => {
      b.schedule ??= structuredClone(defaultSchedule);
      fn(b.schedule);
    });
  const setType = (type: Destination["type"] | "") =>
    edit((b) => {
      if (!type) b.destination = null;
      else b.destination = { type, path: b.destination?.path ?? "", credentialSecretId: type === "smb" ? (b.destination?.credentialSecretId ?? null) : null };
    });

  return (
    <Section
      title="Backups"
      description="The database, the admin certificate and every stored file they reference. After the first backup only new files are copied."
    >
      <div className="grid gap-5 sm:grid-cols-2">
        <Field label="Back up to" path="backup.destination.type">
          {(p) => (
            <Select {...p} value={dest?.type ?? ""} onChange={(e) => setType(e.target.value as Destination["type"] | "")}>
              <option value="">Nowhere (backups off)</option>
              <option value="local">A folder on this server</option>
              <option value="smb">A Windows share (SMB)</option>
            </Select>
          )}
        </Field>
        {dest && (
          <Field
            label={dest.type === "smb" ? "Share" : "Folder"}
            path="backup.destination.path"
            hint={
              dest.type === "smb"
                ? "\\\\server\\share, optionally with a folder, for example \\\\nas01\\backups\\netdeploy."
                : "A full path on this server, outside the data and content folders. Another disk is safer."
            }
          >
            {(p) => (
              <Input
                {...p}
                spellCheck={false}
                className="font-mono"
                value={dest.path}
                onChange={(e) => edit((b) => void (b.destination!.path = e.target.value.trim()))}
              />
            )}
          </Field>
        )}
        {dest?.type === "smb" && (
          <Field
            label="Share credential"
            path="backup.destination.credentialSecretId"
            hint={
              shares.data && shares.data.items.length === 0 ? (
                <>
                  No share credentials yet. <Link className="text-primary underline-offset-4 hover:underline" to="/secrets">Add one on the Secrets page</Link>.
                </>
              ) : (
                <>
                  A secret of the kind share credential. <Link className="text-primary underline-offset-4 hover:underline" to="/secrets">Manage secrets</Link>.
                </>
              )
            }
          >
            {(p) => (
              <Select
                {...p}
                value={dest.credentialSecretId ?? ""}
                onChange={(e) => edit((b) => void (b.destination!.credentialSecretId = e.target.value || null))}
              >
                <option value="">Choose a credential…</option>
                {shares.data?.items.map((x) => (
                  <option key={x.id} value={x.id}>
                    {x.name}
                    {x.hint ? ` · ${x.hint}` : ""}
                  </option>
                ))}
                {dest.credentialSecretId && !shares.data?.items.some((x) => x.id === dest.credentialSecretId) && (
                  <option value={dest.credentialSecretId}>{dest.credentialSecretId}</option>
                )}
              </Select>
            )}
          </Field>
        )}
      </div>

      {dest && (
        <>
          <div className="mt-5 grid gap-5 sm:grid-cols-2">
            <Field label="Schedule" path="backup.schedule.frequency" hint="In this server’s local time. Missed while the server was off: one runs when it starts.">
              {(p) => (
                <Select {...p} value={frequency} onChange={(e) => editSchedule((s) => void (s.frequency = e.target.value as Frequency))}>
                  <option value="disabled">Only when I click Back up now</option>
                  <option value="hourly">Every hour</option>
                  <option value="daily">Every day</option>
                  <option value="weekly">Every week</option>
                  <option value="monthly">Every month</option>
                </Select>
              )}
            </Field>
            {frequency === "hourly" && (
              <Field label="Minutes past the hour" path="backup.schedule.minute" hint="0 to 59.">
                {(p) => (
                  <NumberInput {...p} min={0} max={59} value={schedule.minute} onChange={(v) => editSchedule((s) => void (s.minute = v as number))} />
                )}
              </Field>
            )}
            {(frequency === "daily" || frequency === "weekly" || frequency === "monthly") && (
              <Field label="Time" path="backup.schedule.time" hint="24-hour, for example 02:00.">
                {(p) => (
                  <Input {...p} type="time" value={schedule.time} onChange={(e) => editSchedule((s) => void (s.time = e.target.value))} />
                )}
              </Field>
            )}
            {frequency === "monthly" && (
              <Field label="Day of the month" path="backup.schedule.dayOfMonth" hint="1 to 31. Shorter months use their last day, so 31 means the last day of every month.">
                {(p) => (
                  <NumberInput {...p} min={1} max={31} value={schedule.dayOfMonth} onChange={(v) => editSchedule((s) => void (s.dayOfMonth = v as number))} />
                )}
              </Field>
            )}
            <Field label="Backups to keep" path="backup.retention" hint="Older ones, and files only they use, are deleted after each successful backup. 0 keeps all.">
              {(p) => (
                <NumberInput {...p} min={0} max={1000} value={backup.retention} onChange={(v) => edit((b) => void (b.retention = v as number))} />
              )}
            </Field>
          </div>
          {frequency === "weekly" && (
            <fieldset className="mt-5">
              <legend className="text-sm font-medium">Days</legend>
              <div className="mt-2 flex flex-wrap gap-x-5 gap-y-2">
                {days.map(([d, label]) => {
                  const on = schedule.daysOfWeek.includes(d);
                  return (
                    <Checkbox
                      key={d}
                      label={label}
                      checked={on}
                      onChange={(v) =>
                        editSchedule((s) => {
                          const cur = s.daysOfWeek;
                          const next = v ? [...cur, d] : cur.filter((x) => x !== d);
                          s.daysOfWeek = days.map(([x]) => x).filter((x) => next.includes(x));
                        })
                      }
                    />
                  );
                })}
              </div>
              {schedule.daysOfWeek.length === 0 && <p className="mt-1.5 text-sm text-destructive">Choose at least one day.</p>}
            </fieldset>
          )}
        </>
      )}
    </Section>
  );
}

/**
 * What the saved backup settings have done: status from health, Back up
 * now, and the snapshots at the destination. `pending`: the destination
 * has unsaved edits, so Back up now would use the old one.
 */
export function BackupStatusSection({ saved, pending }: { saved: Backup | undefined; pending: boolean }) {
  const qc = useQueryClient();
  const configured = !!saved?.destination;
  const health = useQuery({
    queryKey: ["health"],
    queryFn: async () => {
      const { data, error } = await api.GET("/system/health");
      return data ?? (error as NonNullable<typeof data>);
    },
  });
  const jobs = useActiveJobs("system.backup");
  const running = jobs.data?.items[0];
  const snapshots = useQuery({
    queryKey: ["backups", saved?.destination?.type, saved?.destination?.path],
    queryFn: async () => unwrap(await api.GET("/system/backups")),
    enabled: configured,
    retry: false,
  });
  const now = useMutation({
    mutationFn: async () => unwrap(await api.POST("/system/backups")),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["jobs"] }),
  });
  const bh = health.data?.backup;
  const keyWarning = health.data?.warnings?.find((w) => w.code === "secrets_key_not_exported");
  const failedLast = !!bh?.lastFailureAt && (!bh.lastSuccessAt || bh.lastFailureAt > bh.lastSuccessAt);

  return (
    <Section
      title="Backup status"
      description="Restoring is done with the server stopped: netdeployd restore -from <destination> -key <key file>."
      actions={
        configured && (
          <Button size="sm" disabled={!!running || now.isPending || pending} onClick={() => now.mutate()}>
            <HardDriveDownload aria-hidden /> {running ? "Backing up…" : "Back up now"}
          </Button>
        )
      }
    >
      {!configured ? (
        <p className="text-sm text-muted-foreground">Backups are off. Choose where to back up to above, then save.</p>
      ) : (
        <div className="space-y-4">
          {pending && <Alert tone="info">Save your changes first: Back up now uses the saved destination.</Alert>}
          {keyWarning && (
            <Alert tone="warning" title="Export the secrets key">
              Backups don’t include the key that encrypts secrets. Without it, secrets can’t be restored on other hardware. Run{" "}
              <code className="font-mono text-xs">netdeployd key export -data &lt;data folder&gt; -out &lt;file&gt;</code> once and keep the file
              somewhere safe, away from this server.
            </Alert>
          )}
          {running && <JobProgress job={running} what="Backup" queuedText="Waiting to start" />}
          {now.isError && <Alert tone="error">{errorMessage(now.error)}</Alert>}
          {failedLast && bh?.lastError && (
            <Alert tone="error" title={`The last backup failed (${formatDateTime(bh.lastFailureAt)})`}>
              {bh.lastError}
            </Alert>
          )}
          <dl className="grid gap-x-6 gap-y-4 text-sm sm:grid-cols-3">
            <div>
              <dt className="text-muted-foreground">Last successful</dt>
              <dd className="mt-0.5">{bh?.lastSuccessAt ? formatDateTime(bh.lastSuccessAt) : "Never"}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Last failed</dt>
              <dd className="mt-0.5">{bh?.lastFailureAt ? formatDateTime(bh.lastFailureAt) : "Never"}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Next scheduled</dt>
              <dd className="mt-0.5">{bh?.nextRunAt ? formatDateTime(bh.nextRunAt) : "Not scheduled"}</dd>
            </div>
          </dl>

          <div>
            <h3 className="text-sm font-semibold">Backups at the destination</h3>
            {snapshots.isPending ? (
              <p className="mt-2 text-sm text-muted-foreground">Reading the destination…</p>
            ) : snapshots.isError ? (
              <Alert tone="error" className="mt-2" title="The destination couldn’t be read">
                {errorMessage(snapshots.error)}
              </Alert>
            ) : snapshots.data.items.length === 0 ? (
              <p className="mt-2 text-sm text-muted-foreground">None yet.</p>
            ) : (
              <div className="mt-2 overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="border-b">
                      <th scope="col" className={th}>Taken</th>
                      <th scope="col" className={th}>How</th>
                      <th scope="col" className={th}>Stored files</th>
                      <th scope="col" className={th}>Database</th>
                      <th scope="col" className={th}>Server</th>
                      <th scope="col" className={`${th} pr-0`}>Secrets key</th>
                    </tr>
                  </thead>
                  <tbody>
                    {snapshots.data.items.map((b, i) => (
                      <tr key={b.id} className="border-b last:border-0">
                        <td className={td}>
                          {formatDateTime(b.createdAt)}
                          {i === 0 && (
                            <span className="ml-2">
                              <Badge>Newest</Badge>
                            </span>
                          )}
                          <div className="font-mono text-xs text-muted-foreground">{b.id}</div>
                        </td>
                        <td className={td}>{triggerText[b.trigger]}</td>
                        <td className={`${td} tabular-nums`}>
                          {b.blobCount.toLocaleString()} · {formatBytes(b.blobBytes)}
                          <div className="text-xs text-muted-foreground">shared with other backups</div>
                        </td>
                        <td className={`${td} tabular-nums`}>{formatBytes(b.databaseBytes)}</td>
                        <td className={td}>
                          {b.serverVersion}
                          <div className="text-xs text-muted-foreground">schema {b.schemaVersion}</div>
                        </td>
                        <td className={`${td} pr-0 font-mono text-xs`} title={b.secretsKeyFingerprint ?? undefined}>
                          {b.secretsKeyFingerprint ? shortHash(b.secretsKeyFingerprint) : <span className="font-sans text-muted-foreground">No secrets</span>}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </div>
      )}
    </Section>
  );
}
