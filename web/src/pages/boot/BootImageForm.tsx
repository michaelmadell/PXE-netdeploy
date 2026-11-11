import { useState, type ReactNode } from "react";
import { Checkbox, Field, FieldErrors, Input, Select, Textarea } from "@/components/ui/form";
import { Alert } from "@/components/ui/misc";
import { ApiError, errorMessage } from "@/lib/problem";
import { signerLabel } from "@/pages/images/data";
import {
  suggestedServerUrls,
  useAdapters,
  useCapabilities,
  useDriverGroups,
  useSettings,
  windowsImages,
  type BootImage,
  type BootImageInput,
  type Image,
} from "./data";

const lines = (t: string) => t.split(/[\s,]+/).filter(Boolean);

/** Only the input fields of a boot image: what create and replace accept. */
function inputOf(b: BootImage): BootImageInput {
  return {
    name: b.name,
    sourceImageId: b.sourceImageId,
    sourceIndex: b.sourceIndex,
    architecture: b.architecture,
    driverGroupIds: b.driverGroupIds ?? [],
    agentVersion: b.agentVersion,
    serverUrls: b.serverUrls,
    builder: b.builder,
    optionalComponents: b.optionalComponents,
    bootmgr: b.bootmgr,
    tftp: b.tftp,
    produceHttpBootIso: b.produceHttpBootIso,
  };
}

/**
 * The fields of a boot image. Used to create one (no `initial`) and to
 * change one. `children` gets the buttons' row; `error` is the failed save.
 */
export function BootImageForm({
  initial,
  images,
  error,
  onSubmit,
  children,
}: {
  initial?: BootImage;
  images: Image[];
  error: unknown;
  onSubmit: (input: BootImageInput) => void;
  children: (state: { dirty: boolean; valid: boolean }) => ReactNode;
}) {
  const sources = windowsImages(images);
  const settings = useSettings();
  const adapters = useAdapters();
  const caps = useCapabilities();
  const groups = useDriverGroups();

  const [draft, setDraft] = useState<BootImageInput>(() =>
    initial
      ? inputOf(initial)
      : {
          name: "",
          sourceImageId: sources.length === 1 ? sources[0]!.image.id : "",
          sourceIndex: 1,
          architecture: sources.length === 1 ? sources[0]!.win.architecture : "x64",
          driverGroupIds: [],
          serverUrls: [],
          builder: "auto",
          bootmgr: "auto",
          produceHttpBootIso: false,
        },
  );
  // Server addresses are suggested until the user types their own.
  const [urlsText, setUrlsText] = useState<string | null>(initial ? initial.serverUrls.join("\n") : null);
  const suggested = suggestedServerUrls(settings.data, adapters.data);
  const urls = urlsText === null ? suggested : lines(urlsText);

  const set = (patch: Partial<BootImageInput>) => setDraft((d) => ({ ...d, ...patch }));
  const source = sources.find((s) => s.image.id === draft.sourceImageId);
  const signers = new Set(source?.win.bootManagers.map((b) => b.signer));
  const has2023 = signers.has("windows-uefi-ca-2023");
  const indexes = source?.win.bootImageIndexes ?? [];
  const input: BootImageInput = { ...draft, name: draft.name.trim(), serverUrls: urls };
  const dirty = !initial || JSON.stringify(input) !== JSON.stringify(inputOf(initial));
  const valid = !!input.name && !!input.sourceImageId && urls.length > 0;
  const builders = caps.data?.builders ?? [];
  const noBuilder = caps.data && !builders.some((b) => b.available);

  return (
    <FieldErrors value={error instanceof ApiError ? error.fields : []}>
      <form
        className="space-y-5"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          if (valid) onSubmit(input);
        }}
      >
        <Field label="Name" path="name" hint="For example “WinPE x64”.">
          {(p) => <Input {...p} maxLength={200} value={draft.name} onChange={(e) => set({ name: e.target.value })} />}
        </Field>

        <Field
          label="Windows image"
          path="sourceImageId"
          hint={
            sources.length === 0
              ? "No Windows image is ready yet. Upload an ISO on the Images page first."
              : "The boot environment (WinPE) and the boot manager are taken from this image’s boot.wim."
          }
        >
          {(p) => (
            <Select
              {...p}
              value={draft.sourceImageId}
              onChange={(e) => {
                const s = sources.find((x) => x.image.id === e.target.value);
                set({ sourceImageId: e.target.value, architecture: s?.win.architecture ?? draft.architecture, sourceIndex: 1, bootmgr: "auto" });
              }}
            >
              <option value="" disabled>
                Choose an image…
              </option>
              {sources.map(({ image, win }) => (
                <option key={image.id} value={image.id}>
                  {image.name} · {win.architecture}
                </option>
              ))}
              {initial && !source && <option value={draft.sourceImageId}>Deleted image</option>}
            </Select>
          )}
        </Field>

        <div className="grid gap-5 sm:grid-cols-2">
          <Field label="Boot manager" path="bootmgr" hint="Secure Boot firmware starts only a boot manager signed by a certificate it trusts.">
            {(p) => (
              <Select {...p} value={draft.bootmgr} onChange={(e) => set({ bootmgr: e.target.value as BootImageInput["bootmgr"] })}>
                <option value="auto">
                  Automatic{source ? ` (${signerLabel[has2023 ? "windows-uefi-ca-2023" : "windows-pca-2011"]})` : ""}
                </option>
                <option value="windows-uefi-ca-2023" disabled={!!source && !has2023}>
                  {signerLabel["windows-uefi-ca-2023"]}
                  {source && !has2023 ? " · not in this image" : ""}
                </option>
                <option value="windows-pca-2011" disabled={!!source && !signers.has("windows-pca-2011")}>
                  {signerLabel["windows-pca-2011"]}
                  {source && !signers.has("windows-pca-2011") ? " · not in this image" : ""}
                </option>
              </Select>
            )}
          </Field>
          <Field label="Built with" path="builder" hint="Automatic prefers wimlib, which needs no administrator rights.">
            {(p) => (
              <Select {...p} value={draft.builder} onChange={(e) => set({ builder: e.target.value as BootImageInput["builder"] })}>
                <option value="auto">Automatic</option>
                {(["wimlib", "dism"] as const).map((name) => {
                  const b = builders.find((x) => x.name === name);
                  return (
                    <option key={name} value={name} disabled={!!b && !b.available}>
                      {name === "dism" ? "DISM" : "wimlib"}
                      {b?.available && b.version ? ` ${b.version}` : ""}
                      {b && !b.available ? " · not available" : ""}
                    </option>
                  );
                })}
              </Select>
            )}
          </Field>
        </div>

        {indexes.length > 1 && (
          <Field
            label="Image inside boot.wim"
            path="sourceIndex"
            hint="Use the plain Windows PE image (normally 1). The Windows Setup image is larger and its Setup program gets in the way."
          >
            {(p) => (
              <Select {...p} value={draft.sourceIndex} onChange={(e) => set({ sourceIndex: Number(e.target.value) })}>
                {indexes.map((b) => (
                  <option key={b.index} value={b.index}>
                    {b.index} · {b.name}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        )}

        <Field
          label="Server addresses"
          path="serverUrls"
          hint={
            urlsText === null && suggested.length === 0
              ? "No deployment interface has HTTP turned on. Turn it on in Settings, or type the address machines will reach this server at, for example http://10.77.0.1:8080."
              : "Where the booted machine finds this server, one per line, tried in order. These are written into the image: if the server’s address or HTTP port changes, rebuild."
          }
        >
          {(p) => (
            <Textarea
              {...p}
              rows={2}
              spellCheck={false}
              className="font-mono text-xs"
              value={urlsText ?? suggested.join("\n")}
              onChange={(e) => setUrlsText(e.target.value)}
            />
          )}
        </Field>

        {(groups.data?.items.length ?? 0) > 0 && (
          <fieldset>
            <legend className="mb-1.5 text-sm font-medium">Drivers to include</legend>
            <p className="mb-3 text-sm text-muted-foreground">
              Network and storage drivers WinPE doesn’t have for your hardware. Most machines need none.
            </p>
            <div className="space-y-2.5">
              {groups.data!.items.map((g) => (
                <Checkbox
                  key={g.id}
                  label={g.name}
                  hint={g.description}
                  checked={draft.driverGroupIds?.includes(g.id) ?? false}
                  onChange={(on) =>
                    set({ driverGroupIds: on ? [...(draft.driverGroupIds ?? []), g.id] : (draft.driverGroupIds ?? []).filter((x) => x !== g.id) })
                  }
                />
              ))}
            </div>
          </fieldset>
        )}

        {noBuilder && (
          <Alert tone="warning" title="This server can’t build boot images yet">
            {builders.map((b) => `${b.name === "dism" ? "DISM" : "wimlib"}: ${b.reason ?? "not available"}`).join(" ")}
          </Alert>
        )}
        {error != null && (
          <Alert tone="error">{error instanceof ApiError && error.fields.length ? "Check the fields above." : errorMessage(error)}</Alert>
        )}
        {children({ dirty, valid })}
      </form>
    </FieldErrors>
  );
}
