import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Field, FieldErrors, Input, Select, Textarea } from "@/components/ui/form";
import { Alert } from "@/components/ui/misc";
import { ApiError, unwrap } from "@/lib/problem";
import { clientError, clientInput, refreshClients, useProfiles, useSecrets, type Client, type ClientInput } from "./data";

const macPattern = /^([0-9a-f]{2}[:-]){5}[0-9a-f]{2}$/i;
const ipv4 = /^(25[0-5]|2[0-4]\d|1?\d?\d)(\.(25[0-5]|2[0-4]\d|1?\d?\d)){3}$/;

/** "key=value" per line. */
function parseVariables(text: string): { vars: Record<string, string>; bad: string[] } {
  const vars: Record<string, string> = {};
  const bad: string[] = [];
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (!line) continue;
    const i = line.indexOf("=");
    if (i <= 0) bad.push(line);
    else vars[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  return { vars, bad };
}

/** Add a client, or edit one. Hardware details the agent reported are kept. */
export function ClientForm({ client, onDone, onCancel }: { client: Client | null; onDone: (c: Client) => void; onCancel: () => void }) {
  const profiles = useProfiles();
  const blobs = useSecrets("odj-blob");
  const [mac, setMac] = useState(client?.mac ?? "");
  const [hostname, setHostname] = useState(client?.hostname ?? "");
  const [profileId, setProfileId] = useState(client?.profileId ?? "");
  const [reservedIp, setReservedIp] = useState(client?.reservedIp ?? "");
  const [odjBlob, setOdjBlob] = useState(client?.secretRefs?.odjBlob ?? "");
  const [varsText, setVarsText] = useState(
    Object.entries(client?.variables ?? {})
      .map(([k, v]) => `${k}=${v}`)
      .join("\n"),
  );
  const { vars, bad } = parseVariables(varsText);
  const macOk = macPattern.test(mac.trim());
  const ipOk = !reservedIp.trim() || ipv4.test(reservedIp.trim());
  const valid = macOk && ipOk && bad.length === 0;

  const save = useMutation({
    mutationFn: async () => {
      const input: ClientInput = {
        ...(client ? clientInput(client) : {}),
        mac: mac.trim(),
        hostname: hostname.trim() || null,
        profileId: profileId || null,
        reservedIp: reservedIp.trim() || null,
        variables: vars,
        secretRefs: { odjBlob: odjBlob || null },
      };
      return client
        ? unwrap(await api.PUT("/clients/{clientId}", { params: { path: { clientId: client.id! }, header: { "If-Match": client.etag! } }, body: input }))
        : unwrap(await api.POST("/clients", { body: input }));
    },
    onSuccess: (c) => {
      refreshClients();
      onDone(c);
    },
  });
  const stale = save.error instanceof ApiError && save.error.status === 412;

  return (
    <FieldErrors value={save.error instanceof ApiError ? save.error.fields : []}>
      <form
        className="space-y-5"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          if (valid) save.mutate();
        }}
      >
        <div className="grid gap-5 sm:grid-cols-2">
          <Field label="MAC address" path="mac" hint={mac && !macOk ? "Six pairs of hex digits, e.g. 00:15:5d:00:85:09." : "Of the network adapter the machine boots from."}>
            {(p) => <Input {...p} className="font-mono" value={mac} onChange={(e) => setMac(e.target.value)} placeholder="00:15:5d:00:85:09" />}
          </Field>
          <Field label="Name" path="hostname" hint="For you; it isn’t the computer name Windows gets.">
            {(p) => <Input {...p} value={hostname} onChange={(e) => setHostname(e.target.value)} />}
          </Field>
        </div>
        <Field label="Profile" path="profileId" hint="What this machine gets when it’s deployed, and what the boot prompt offers it first.">
          {(p) => (
            <Select {...p} value={profileId} onChange={(e) => setProfileId(e.target.value)}>
              <option value="">None (the default profile)</option>
              {profiles.data?.items.map((pr) => (
                <option key={pr.id} value={pr.id}>
                  {pr.name}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <div className="grid gap-5 sm:grid-cols-2">
          <Field label="Reserved address" path="reservedIp" hint={!ipOk ? "An IPv4 address, e.g. 10.77.0.50." : "Optional. Always given this address by the built-in DHCP."}>
            {(p) => <Input {...p} className="font-mono" value={reservedIp} onChange={(e) => setReservedIp(e.target.value)} placeholder="10.77.0.50" />}
          </Field>
          <Field label="Offline domain join blob" path="secretRefs.odjBlob" hint="Only for profiles that join a domain offline. Blobs are added as secrets.">
            {(p) => (
              <Select {...p} value={odjBlob} onChange={(e) => setOdjBlob(e.target.value)}>
                <option value="">None</option>
                {blobs.data?.items.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name}
                    {s.hint ? ` (${s.hint})` : ""}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        </div>
        <Field
          label="Variables"
          path="variables"
          hint={bad.length ? `Each line needs name=value: ${bad.join(", ")}` : "One name=value per line, for computer name patterns ({{ .Variables.name }}). They override the profile’s."}
        >
          {(p) => <Textarea {...p} className="font-mono" rows={3} value={varsText} onChange={(e) => setVarsText(e.target.value)} placeholder="site=ldn" />}
        </Field>

        {save.isError && (
          <Alert tone="error" title={stale ? "Changed somewhere else" : undefined}>
            {stale ? "This client was changed since you opened it. Close this and open it again." : clientError(save.error)}
          </Alert>
        )}
        <div className="flex justify-end gap-2">
          <Button onClick={onCancel}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!valid || save.isPending}>
            {save.isPending ? "Saving…" : client ? "Save" : "Add client"}
          </Button>
        </div>
      </form>
    </FieldErrors>
  );
}
