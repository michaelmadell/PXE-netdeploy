import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { useEffect, useId, useMemo, useState } from "react";
import { api, type Schemas } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Checkbox, Field, FieldErrors, Input, NumberInput, RadioCards, Select, useFieldErrors } from "@/components/ui/form";
import { Alert, PageHeader, Section, td, th } from "@/components/ui/misc";
import { formatBytes } from "@/lib/format";
import { ApiError, errorMessage, unwrap, type FieldError } from "@/lib/problem";

type Settings = Schemas["Settings"];
type Iface = Schemas["InterfaceSettings"];
type Pool = NonNullable<Iface["dhcp"]["pool"]>;
type HostInterface = Schemas["HostInterface"];

const cidr = (a: HostInterface["addresses"][number]) => `${a.address}/${a.prefixLength}`;

/** 24 → "255.255.255.0". */
function maskFromPrefix(bits: number): string {
  const m = bits <= 0 ? 0 : (0xffffffff << (32 - bits)) >>> 0;
  return [m >>> 24, (m >>> 16) & 255, (m >>> 8) & 255, m & 255].join(".");
}

/** "vEthernet (Lab) · 10.77.0.1/24", with the reason when it can't be used as it is. */
function adapterLabel(h: HostInterface): string {
  const addrs = h.addresses.length ? h.addresses.map(cidr).join(", ") : "no IPv4 address";
  return `${h.name} · ${addrs}${h.up ? "" : " · not connected"}`;
}

const GiB = 1024 ** 3;

const newPool = (): Pool => ({
  start: "",
  end: "",
  subnetMask: "255.255.255.0",
  router: null,
  dnsServers: [],
  domainName: null,
  leaseSeconds: 3600,
  pingCheck: true,
});

const newInterface = (): Iface => ({
  name: "",
  macPrefixes: [],
  advertisedAddress: null,
  services: { tftp: true, http: true, httpPort: 80, https: false, httpsPort: 443 },
  dhcp: { mode: "proxy", listenOn67: true, pool: null },
});

/**
 * What is sent: the draft without the editing conveniences. A pool is kept
 * in the draft while the mode is switched back and forth, but only `full`
 * has one; empty optional text becomes null; an empty password is "no change".
 */
function toRequest(draft: Settings): Settings {
  const s = structuredClone(draft);
  for (const i of s.interfaces) {
    i.name = i.name.trim();
    i.advertisedAddress = i.advertisedAddress?.trim() || null;
    if (i.dhcp.mode !== "full") i.dhcp.pool = null;
    if (i.dhcp.pool) {
      i.dhcp.pool.router = i.dhcp.pool.router?.trim() || null;
      i.dhcp.pool.domainName = i.dhcp.pool.domainName?.trim() || null;
    }
  }
  if (s.bootPrompt && !s.bootPrompt.password) delete s.bootPrompt.password;
  return s;
}

/** Comma- or space-separated text ⇄ string list. */
function ListInput({
  value,
  onChange,
  normalise = (s) => s,
  ...props
}: {
  value: string[];
  onChange: (v: string[]) => void;
  normalise?: (s: string) => string;
  id?: string;
  placeholder?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: true;
}) {
  const parse = (t: string) => t.split(/[\s,;]+/).filter(Boolean).map(normalise);
  const [text, setText] = useState(value.join(", "));
  useEffect(() => {
    setText((cur) => (parse(cur).join() === value.join() ? cur : value.join(", ")));
  }, [value]);
  return (
    <Input
      {...props}
      value={text}
      spellCheck={false}
      autoComplete="off"
      onChange={(e) => {
        setText(e.target.value);
        onChange(parse(e.target.value));
      }}
    />
  );
}

const ipProps = { inputMode: "decimal", spellCheck: false, autoComplete: "off" } as const;

export function SettingsPage() {
  const qc = useQueryClient();
  const settings = useQuery({
    queryKey: ["settings"],
    queryFn: async () => unwrap(await api.GET("/settings")),
  });
  // The server's adapters, re-read whenever the page is opened: they change with cables and virtual switches.
  const adapters = useQuery({
    queryKey: ["host-interfaces"],
    queryFn: async () => unwrap(await api.GET("/system/interfaces")).items.filter((h) => !h.loopback),
    staleTime: 0,
  });
  const profiles = useQuery({
    queryKey: ["profiles"],
    queryFn: async () => unwrap(await api.GET("/profiles", { params: { query: { limit: 200 } } })),
  });

  // `base` is the server's copy the draft was made from; its etag goes in If-Match.
  const [base, setBase] = useState<Settings | null>(null);
  const [draft, setDraft] = useState<Settings | null>(null);
  const [saved, setSaved] = useState(false);

  const dirty = useMemo(
    () => !!base && !!draft && JSON.stringify(toRequest(draft)) !== JSON.stringify(toRequest(base)),
    [base, draft],
  );
  const changedElsewhere = dirty && !!settings.data && settings.data.etag !== base?.etag;

  // Take the server's copy whenever there are no unsaved edits to lose.
  useEffect(() => {
    if (settings.data && !dirty && settings.data !== base) {
      setBase(settings.data);
      setDraft(structuredClone(settings.data));
    }
  }, [settings.data, dirty, base]);

  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const save = useMutation({
    mutationFn: async (s: Settings) =>
      unwrap(await api.PUT("/settings", { params: { header: { "If-Match": base?.etag ?? "*" } }, body: toRequest(s) })),
    onSuccess: (data) => {
      setBase(data);
      setDraft(structuredClone(data));
      setSaved(true);
      qc.setQueryData(["settings"], data);
      void qc.invalidateQueries({ queryKey: ["health"] });
    },
  });

  const edit = (fn: (s: Settings) => void) => {
    setSaved(false);
    setDraft((cur) => {
      if (!cur) return cur;
      const next = structuredClone(cur);
      fn(next);
      return next;
    });
  };

  const reload = async () => {
    save.reset();
    const fresh = await settings.refetch();
    if (fresh.data) {
      setBase(fresh.data);
      setDraft(structuredClone(fresh.data));
    }
  };

  if (settings.isPending) return <p className="text-muted-foreground">Loading settings…</p>;
  if (settings.isError || !draft || !base)
    return (
      <Alert tone="error" title="Settings couldn’t be loaded">
        {errorMessage(settings.error)}
      </Alert>
    );

  const fieldErrors: FieldError[] = save.error instanceof ApiError ? save.error.fields : [];
  const stale = (save.error instanceof ApiError && save.error.status === 412) || changedElsewhere;
  const adminMoved = draft.admin.bindAddress !== base.admin.bindAddress || draft.admin.port !== base.admin.port;
  const bp = draft.bootPrompt;
  const password = bp?.password ?? "";

  return (
    <FieldErrors value={fieldErrors}>
      <form
        className="max-w-4xl pb-24"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate(draft);
        }}
      >
        <PageHeader
          title="Settings"
          description="Changes apply together when you save. If a listener can’t start, nothing changes and the running configuration is kept."
        />

        <div className="space-y-6">
          <Section
            title="Deployment interfaces"
            description="Network adapters that face the machines you deploy to. Each has its own DHCP mode and serves boot files over TFTP and HTTP."
            actions={
              <Button size="sm" onClick={() => edit((s) => s.interfaces.push(newInterface()))}>
                <Plus aria-hidden /> Add interface
              </Button>
            }
          >
            {draft.interfaces.length === 0 ? (
              <p className="text-sm text-muted-foreground">
                No interfaces yet. Add the adapter connected to your deployment network; until then nothing answers
                network boot requests.
              </p>
            ) : (
              <div className="space-y-5">
                {draft.interfaces.map((iface, i) => (
                  <InterfaceCard
                    key={i}
                    index={i}
                    iface={iface}
                    saved={base.interfaces.find((b) => b.name === iface.name)}
                    adapters={adapters.data}
                    adaptersLoading={adapters.isPending}
                    usedElsewhere={draft.interfaces.filter((_, n) => n !== i).map((o) => o.name)}
                    edit={(fn) => edit((s) => fn(s.interfaces[i]!))}
                    remove={() => edit((s) => void s.interfaces.splice(i, 1))}
                  />
                ))}
              </div>
            )}
          </Section>

          {draft.tftp && (
            <Section
              title="TFTP"
              description="Limits for every transfer. Clients negotiate up to these; lower them if boot files stall on your network."
            >
              <div className="grid gap-5 sm:grid-cols-3">
                <Field label="Maximum block size" path="tftp.maxBlockSize" hint="Bytes, 512 to 65464. Default 8192.">
                  {(p) => (
                    <NumberInput
                      {...p}
                      min={512}
                      max={65464}
                      value={draft.tftp!.maxBlockSize}
                      onChange={(v) => edit((s) => void (s.tftp!.maxBlockSize = v as number))}
                    />
                  )}
                </Field>
                <Field label="Maximum window size" path="tftp.maxWindowSize" hint="Blocks sent before waiting for an acknowledgement, 1 to 64. Default 8.">
                  {(p) => (
                    <NumberInput
                      {...p}
                      min={1}
                      max={64}
                      value={draft.tftp!.maxWindowSize}
                      onChange={(v) => edit((s) => void (s.tftp!.maxWindowSize = v as number))}
                    />
                  )}
                </Field>
                <Field label="Retransmit timeout" path="tftp.timeoutSeconds" hint="Seconds, 1 to 60. Each lost block costs one timeout.">
                  {(p) => (
                    <NumberInput
                      {...p}
                      min={1}
                      max={60}
                      value={draft.tftp!.timeoutSeconds}
                      onChange={(v) => edit((s) => void (s.tftp!.timeoutSeconds = v as number))}
                    />
                  )}
                </Field>
              </div>
            </Section>
          )}

          {bp && (
            <Section
              title="Boot prompt"
              description="What a network-booted machine shows when no deployment is scheduled for it. Without the password it counts down and starts from its own disk."
            >
              <div className="grid gap-5 sm:grid-cols-2">
                <Field
                  label={bp.hasPassword ? "Change password" : "Set password"}
                  path="bootPrompt.password"
                  hint={
                    password && password.length < 8
                      ? "At least 8 characters."
                      : bp.hasPassword
                        ? "A password is set. Leave empty to keep it."
                        : "No password is set: only scheduled deployments can run. At least 8 characters."
                  }
                >
                  {(p) => (
                    <Input
                      {...p}
                      type="password"
                      autoComplete="new-password"
                      value={password}
                      onChange={(e) => edit((s) => void (s.bootPrompt!.password = e.target.value))}
                    />
                  )}
                </Field>
                <Field
                  label="Default profile"
                  path="bootPrompt.defaultProfileId"
                  hint="Offered to machines that have no profile of their own. With none, unknown machines are not answered."
                >
                  {(p) => (
                    <Select
                      {...p}
                      value={bp.defaultProfileId ?? ""}
                      onChange={(e) => edit((s) => void (s.bootPrompt!.defaultProfileId = e.target.value || null))}
                    >
                      <option value="">None</option>
                      {profiles.data?.items.map((pr) => (
                        <option key={pr.id} value={pr.id}>
                          {pr.name}
                        </option>
                      ))}
                      {bp.defaultProfileId && !profiles.data?.items.some((pr) => pr.id === bp.defaultProfileId) && (
                        <option value={bp.defaultProfileId}>{bp.defaultProfileId}</option>
                      )}
                    </Select>
                  )}
                </Field>
                <Field label="Prompt countdown" path="bootPrompt.timeoutSeconds" hint="Seconds before the machine starts from its disk, 5 to 3600.">
                  {(p) => (
                    <NumberInput
                      {...p}
                      min={5}
                      max={3600}
                      value={bp.timeoutSeconds}
                      onChange={(v) => edit((s) => void (s.bootPrompt!.timeoutSeconds = v as number))}
                    />
                  )}
                </Field>
                <Field
                  label="Scheduled deployment countdown"
                  path="bootPrompt.armedCountdownSeconds"
                  hint="Seconds before a scheduled deployment starts, 0 to 300. Any key opens the menu instead."
                >
                  {(p) => (
                    <NumberInput
                      {...p}
                      min={0}
                      max={300}
                      value={bp.armedCountdownSeconds}
                      onChange={(v) => edit((s) => void (s.bootPrompt!.armedCountdownSeconds = v as number))}
                    />
                  )}
                </Field>
              </div>
              <Checkbox
                className="mt-5"
                label="Require the password"
                hint="Turn off for debugging only."
                checked={bp.requirePassword}
                onChange={(v) => edit((s) => void (s.bootPrompt!.requirePassword = v))}
              />
              {!bp.requirePassword && (
                <Alert tone="warning" className="mt-3" title="Anyone at a machine on a deployment network can re-image it">
                  The profile menu opens without a password while this is off.
                </Alert>
              )}
            </Section>
          )}

          {draft.architectures && (
            <Section title="Client types" description="Which kinds of machine get an answer when they network boot.">
              <div className="grid gap-4 sm:grid-cols-2">
                <Checkbox
                  label="UEFI x64"
                  checked={draft.architectures.uefiX64}
                  onChange={(v) => edit((s) => void (s.architectures!.uefiX64 = v))}
                />
                <Checkbox
                  label="UEFI ARM64"
                  checked={draft.architectures.uefiArm64}
                  onChange={(v) => edit((s) => void (s.architectures!.uefiArm64 = v))}
                />
                <Checkbox
                  label="UEFI HTTP Boot"
                  hint="Firmware that downloads its boot file over HTTP instead of TFTP."
                  checked={draft.architectures.httpBoot}
                  onChange={(v) => edit((s) => void (s.architectures!.httpBoot = v))}
                />
                <Checkbox
                  label="Legacy BIOS"
                  hint="Windows 11 needs UEFI, so this is normally off."
                  checked={draft.architectures.bios}
                  onChange={(v) => edit((s) => void (s.architectures!.bios = v))}
                />
              </div>
            </Section>
          )}

          {draft.storage && (
            <Section title="Storage" description="Limits for the content store, where imported images, drivers and uploads are kept.">
              <div className="grid gap-5 sm:grid-cols-2">
                <Field
                  label="Free space to keep (GiB)"
                  path="storage.reserveBytes"
                  hint={`Uploads and imports that would leave less than this are refused. Now ${formatBytes(base.storage?.reserveBytes)}.`}
                >
                  {(p) => (
                    <NumberInput
                      {...p}
                      min={0}
                      step="any"
                      value={draft.storage!.reserveBytes / GiB}
                      onChange={(v) => edit((s) => void (s.storage!.reserveBytes = v === undefined ? (v as never) : Math.round(v * GiB)))}
                    />
                  )}
                </Field>
                <Field
                  label="Keep unused uploads for (hours)"
                  path="storage.uploadExpiryHours"
                  hint="Uploads that were never imported are deleted this long after their last activity, 1 to 720."
                >
                  {(p) => (
                    <NumberInput
                      {...p}
                      min={1}
                      max={720}
                      value={draft.storage!.uploadExpiryHours}
                      onChange={(v) => edit((s) => void (s.storage!.uploadExpiryHours = v as number))}
                    />
                  )}
                </Field>
              </div>
            </Section>
          )}

          <Section
            title="Admin listener"
            description="Where this web UI and the admin API listen (always HTTPS). Keep it on a management address that deployment networks can’t reach."
          >
            <div className="grid gap-5 sm:grid-cols-2">
              <Field label="Address" path="admin.bindAddress" hint="An IPv4 address of this server. 127.0.0.1 allows this machine only.">
                {(p) => (
                  <Input
                    {...p}
                    {...ipProps}
                    value={draft.admin.bindAddress}
                    onChange={(e) => edit((s) => void (s.admin.bindAddress = e.target.value.trim()))}
                  />
                )}
              </Field>
              <Field label="Port" path="admin.port">
                {(p) => (
                  <NumberInput
                    {...p}
                    min={1}
                    max={65535}
                    value={draft.admin.port}
                    onChange={(v) => edit((s) => void (s.admin.port = v as number))}
                  />
                )}
              </Field>
            </div>
            {adminMoved && (
              <Alert tone="warning" className="mt-4" title="This page will move">
                After saving, open https://{draft.admin.bindAddress === "0.0.0.0" ? "<this server>" : draft.admin.bindAddress}:
                {draft.admin.port}. The old address closes a few seconds later. If you lock yourself out, start
                netdeployd with -admin-addr.
              </Alert>
            )}
          </Section>
        </div>

        {/* Save bar: always in view, so a long form never hides its own result. */}
        <div className="fixed inset-x-0 bottom-0 z-10 border-t bg-background/95 backdrop-blur md:left-60">
          <div className="flex max-w-4xl flex-wrap items-center gap-x-4 gap-y-2 px-8 py-3">
            <div className="min-w-0 flex-1 text-sm" aria-live="polite">
              {stale ? (
                <span className="text-destructive">
                  Settings were changed somewhere else. Reload to see them; your edits here will be lost.
                </span>
              ) : save.isError ? (
                <div role="alert" className="text-destructive">
                  <p className="font-medium">
                    {fieldErrors.length > 0 ? "Not saved. Nothing was changed:" : `Not saved. ${errorMessage(save.error)}`}
                  </p>
                  {fieldErrors.length > 0 && (
                    <ul className="mt-1 list-disc pl-5">
                      {fieldErrors.map((e, i) => (
                        <li key={i}>
                          {e.field && <code className="font-mono text-xs">{e.field}</code>} {e.message}
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
              ) : dirty ? (
                <span className="text-muted-foreground">Unsaved changes.</span>
              ) : saved ? (
                <span className="text-muted-foreground">Saved and applied.</span>
              ) : null}
            </div>
            {stale ? (
              <Button onClick={() => void reload()}>Reload settings</Button>
            ) : (
              <>
                <Button
                  disabled={!dirty || save.isPending}
                  onClick={() => {
                    save.reset();
                    setDraft(structuredClone(base));
                  }}
                >
                  Discard
                </Button>
                <Button type="submit" variant="primary" disabled={!dirty || save.isPending}>
                  {save.isPending ? "Applying…" : "Save and apply"}
                </Button>
              </>
            )}
          </div>
        </div>
      </form>
    </FieldErrors>
  );
}

function InterfaceCard({
  index,
  iface,
  saved,
  adapters,
  adaptersLoading,
  usedElsewhere,
  edit,
  remove,
}: {
  index: number;
  iface: Iface;
  /** The saved interface of the same name, for the external DHCP guidance. */
  saved: Iface | undefined;
  /** The server's adapters; undefined if they couldn't be read (the name is then typed). */
  adapters: HostInterface[] | undefined;
  adaptersLoading: boolean;
  /** Adapters other interface cards already use. */
  usedElsewhere: string[];
  edit: (fn: (i: Iface) => void) => void;
  remove: () => void;
}) {
  const path = `interfaces[${index}]`;
  const { dhcp, services } = iface;
  const pool = dhcp.pool;
  const guidance = saved?.dhcp.mode === "off" ? (saved.externalDhcpGuidance ?? []) : [];
  const editPool = (fn: (p: Pool) => void) => edit((i) => fn(i.dhcp.pool!));
  const adapter = adapters?.find((h) => h.name === iface.name);
  const missing = !!adapters && !!iface.name && !adapter;
  const addressListId = useId();

  return (
    <fieldset className="rounded-md border p-4">
      <legend className="px-1.5 text-sm font-semibold">{iface.name || `Interface ${index + 1}`}</legend>
      <div className="grid gap-5 sm:grid-cols-2">
        {adapters || adaptersLoading ? (
          <Field
            label="Network adapter"
            path={`${path}.name`}
            hint={
              missing
                ? `This server has no adapter called “${iface.name}” now. Choose another, or reconnect it.`
                : adapter && adapter.addresses.length === 0
                  ? "This adapter has no IPv4 address, so it can’t be saved yet. Give it one in the operating system first."
                  : adapter && !adapter.up
                    ? "This adapter is not connected. Clients can’t reach it until it is."
                    : adapter?.mac
                      ? `MAC ${adapter.mac}`
                      : "The adapter connected to the network your machines boot from."
            }
          >
            {(p) => (
              <Select {...p} disabled={!adapters} value={iface.name} onChange={(e) => edit((i) => void (i.name = e.target.value))}>
                <option value="" disabled>
                  {adapters ? "Choose an adapter…" : "Reading adapters…"}
                </option>
                {missing && <option value={iface.name}>{iface.name} · not found on this server</option>}
                {adapters?.map((h) => (
                  <option key={h.name} value={h.name} disabled={usedElsewhere.includes(h.name)}>
                    {adapterLabel(h)}
                    {usedElsewhere.includes(h.name) ? " · already added" : ""}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        ) : (
          // The list couldn't be read (an older server): fall back to typing the name.
          <Field
            label="Adapter name"
            path={`${path}.name`}
            hint="As the operating system names it, for example “Ethernet 2” or “enp3s0”. If the name is wrong, saving lists the adapters this server has."
          >
            {(p) => (
              <Input {...p} spellCheck={false} autoComplete="off" value={iface.name} onChange={(e) => edit((i) => void (i.name = e.target.value))} />
            )}
          </Field>
        )}
        <Field
          label="Address clients use (optional)"
          path={`${path}.advertisedAddress`}
          hint={
            adapter?.addresses[0]
              ? `Leave empty to use ${adapter.addresses[0].address}. Set it when the adapter has several addresses, or clients reach this server through NAT.`
              : "Leave empty to use the adapter’s first IPv4 address. Set it when the adapter has several, or clients reach this server through NAT."
          }
        >
          {(p) => (
            <Input
              {...p}
              {...ipProps}
              placeholder={adapter?.addresses[0] ? `Automatic (${adapter.addresses[0].address})` : "Automatic"}
              list={addressListId}
              value={iface.advertisedAddress ?? ""}
              onChange={(e) => edit((i) => void (i.advertisedAddress = e.target.value))}
            />
          )}
        </Field>
        <datalist id={addressListId}>
          {adapter?.addresses.map((a) => <option key={a.address} value={a.address} />)}
        </datalist>
      </div>

      <div className="mt-5">
        <RadioCards
          legend="DHCP"
          value={dhcp.mode}
          onChange={(mode) =>
            edit((i) => {
              i.dhcp.mode = mode;
              if (mode === "full" && !i.dhcp.pool) {
                i.dhcp.pool = newPool();
                const primary = adapter?.addresses[0];
                if (primary) i.dhcp.pool.subnetMask = maskFromPrefix(primary.prefixLength);
              }
            })
          }
          options={[
            { value: "proxy", label: "Proxy", description: "Works beside your existing DHCP server. Answers network boot requests only; never assigns addresses." },
            { value: "full", label: "Full", description: "This server is the DHCP server here: it assigns addresses from a pool. Use on an isolated network." },
            { value: "off", label: "Off", description: "Your own DHCP server points clients at this one. You set its boot options yourself." },
          ]}
        />
        <FieldMessages path={`${path}.dhcp`} />
        <FieldMessages path={`${path}.dhcp.mode`} />
      </div>

      {dhcp.mode === "proxy" && (
        <Checkbox
          className="mt-4"
          label="Listen on UDP port 67"
          hint="Turn off only when a DHCP server on this same machine owns port 67. The proxy then uses port 4011 alone, and clients find it only if that server sends option 60 “PXEClient”."
          checked={dhcp.listenOn67}
          onChange={(v) => edit((i) => void (i.dhcp.listenOn67 = v))}
        />
      )}

      {dhcp.mode === "full" && pool && (
        <div className="mt-4 rounded-md bg-muted/50 p-4">
          <FieldMessages path={`${path}.dhcp.pool`} />
          <div className="grid gap-5 sm:grid-cols-3">
            <Field label="First address" path={`${path}.dhcp.pool.start`}>
              {(p) => <Input {...p} {...ipProps} placeholder="10.77.0.100" value={pool.start} onChange={(e) => editPool((x) => void (x.start = e.target.value.trim()))} />}
            </Field>
            <Field label="Last address" path={`${path}.dhcp.pool.end`}>
              {(p) => <Input {...p} {...ipProps} placeholder="10.77.0.200" value={pool.end} onChange={(e) => editPool((x) => void (x.end = e.target.value.trim()))} />}
            </Field>
            <Field label="Subnet mask" path={`${path}.dhcp.pool.subnetMask`}>
              {(p) => <Input {...p} {...ipProps} value={pool.subnetMask} onChange={(e) => editPool((x) => void (x.subnetMask = e.target.value.trim()))} />}
            </Field>
            <Field label="Router (optional)" path={`${path}.dhcp.pool.router`}>
              {(p) => <Input {...p} {...ipProps} value={pool.router ?? ""} onChange={(e) => editPool((x) => void (x.router = e.target.value))} />}
            </Field>
            <Field label="DNS servers (optional)" path={`${path}.dhcp.pool.dnsServers`} hint="Separate with commas.">
              {(p) => <ListInput {...p} value={pool.dnsServers ?? []} onChange={(v) => editPool((x) => void (x.dnsServers = v))} />}
            </Field>
            <Field label="Domain name (optional)" path={`${path}.dhcp.pool.domainName`}>
              {(p) => <Input {...p} spellCheck={false} value={pool.domainName ?? ""} onChange={(e) => editPool((x) => void (x.domainName = e.target.value))} />}
            </Field>
            <Field label="Lease time (seconds)" path={`${path}.dhcp.pool.leaseSeconds`} hint="At least 60. Default 3600.">
              {(p) => <NumberInput {...p} min={60} value={pool.leaseSeconds} onChange={(v) => editPool((x) => void (x.leaseSeconds = v as number))} />}
            </Field>
            <Checkbox
              className="self-center sm:col-span-2"
              label="Ping an address before offering it"
              hint="Avoids handing out an address something else already uses."
              checked={pool.pingCheck}
              onChange={(v) => editPool((x) => void (x.pingCheck = v))}
            />
          </div>
        </div>
      )}

      {dhcp.mode === "off" && (
        <div className="mt-4 rounded-md bg-muted/50 p-4 text-sm">
          {guidance.length > 0 ? (
            <>
              <p className="font-medium">Set these on your DHCP server</p>
              <div className="mt-2 overflow-x-auto">
                <table className="w-full">
                  <thead>
                    <tr className="border-b">
                      <th scope="col" className={th}>Client type</th>
                      <th scope="col" className={th}>Next server (66)</th>
                      <th scope="col" className={th}>Boot file (67)</th>
                      <th scope="col" className={th}>Vendor class (60)</th>
                    </tr>
                  </thead>
                  <tbody>
                    {guidance.map((g, n) => (
                      <tr key={n} className="border-b last:border-0">
                        <td className={td}>{g.arch}</td>
                        <td className={`${td} font-mono text-xs`}>{g.nextServer || "–"}</td>
                        <td className={`${td} font-mono text-xs break-all`}>{g.bootFile || "–"}</td>
                        <td className={`${td} font-mono text-xs`}>{g.vendorClass || "–"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          ) : (
            <p className="text-muted-foreground">Save to see the options to set on your DHCP server.</p>
          )}
        </div>
      )}

      <div className="mt-5 grid gap-5 sm:grid-cols-2">
        <div className="space-y-3">
          <p className="text-sm font-medium">Services</p>
          <Checkbox label="TFTP" hint="Boot files for PXE firmware and the Windows boot manager." checked={services.tftp} onChange={(v) => edit((i) => void (i.services.tftp = v))} />
          <FieldMessages path={`${path}.services.tftp`} />
          <Checkbox label="HTTP" hint="Boot files, images and drivers for the agent, and HTTP Boot." checked={services.http} onChange={(v) => edit((i) => void (i.services.http = v))} />
          <FieldMessages path={`${path}.services.http`} />
          <FieldMessages path={`${path}.services`} />
        </div>
        <div className="space-y-5">
          <Field label="HTTP port" path={`${path}.services.httpPort`}>
            {(p) => (
              <NumberInput {...p} min={1} max={65535} disabled={!services.http} value={services.httpPort} onChange={(v) => edit((i) => void (i.services.httpPort = v as number))} />
            )}
          </Field>
          {dhcp.mode !== "off" && (
            <Field
              label="Only answer these MAC prefixes (optional)"
              path={`${path}.macPrefixes`}
              hint="For example 00:15:5d. Separate with commas. Empty answers every network boot client."
            >
              {(p) => (
                <ListInput
                  {...p}
                  value={iface.macPrefixes ?? []}
                  normalise={(s) => s.toLowerCase().replaceAll("-", ":")}
                  onChange={(v) => edit((i) => void (i.macPrefixes = v))}
                />
              )}
            </Field>
          )}
        </div>
      </div>

      <FieldMessages path={path} />
      <div className="mt-4 flex justify-end">
        <Button variant="destructive-ghost" size="sm" onClick={remove}>
          <Trash2 aria-hidden /> Remove interface
        </Button>
      </div>
    </fieldset>
  );
}

/** Server errors for a path that has no single control (a bind failure on `interfaces[0].dhcp`). */
function FieldMessages({ path }: { path: string }) {
  const errors = useFieldErrors(path);
  if (errors.length === 0) return null;
  return <p className="mt-1.5 text-sm text-destructive">{errors.join(" ")}</p>;
}
