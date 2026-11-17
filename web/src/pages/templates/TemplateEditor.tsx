import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Copy, LoaderCircle, RotateCcw, Save } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { api, type Schemas } from "@/api/client";
import { CodeEditor, type CodeEditorHandle } from "@/components/CodeEditor";
import { Button } from "@/components/ui/button";
import { Checkbox, Field, Input, Select, Textarea } from "@/components/ui/form";
import { Alert, Badge, PageHeader, Section } from "@/components/ui/misc";
import { ApiError, errorMessage, unwrap } from "@/lib/problem";
import { cn } from "@/lib/utils";
import { useClients, useProfiles, clientName } from "@/pages/clients/data";
import { parseVariables, VariablesField } from "@/pages/profiles/IdentityFields";
import {
  BUILTIN,
  modeText,
  refreshTemplates,
  templateError,
  templateErrorLine,
  templateInput,
  templateModes,
  useDebounced,
  usePreview,
  useTemplate,
  type Mode,
  type TemplateInput,
} from "./data";
import { Reference } from "./Reference";

const varsText = (v: Record<string, string> | undefined) =>
  Object.entries(v ?? {})
    .map(([k, x]) => `${k}=${x}`)
    .join("\n");
const requiredList = (text: string) => [...new Set(text.split(/[\n,]/).map((s) => s.trim()).filter(Boolean))];

type Draft = { name: string; description: string; content: string; modes: Mode[]; defaults: string; required: string };

function toDraft(t: TemplateInput): Draft {
  return {
    name: t.name,
    description: t.description ?? "",
    content: t.content,
    modes: t.identityModes,
    defaults: varsText(t.variableDefaults),
    required: (t.requiredVariables ?? []).join("\n"),
  };
}
function toInput(d: Draft): TemplateInput {
  return {
    name: d.name.trim(),
    description: d.description.trim(),
    content: d.content,
    identityModes: templateModes.filter((m) => d.modes.includes(m)),
    variableDefaults: parseVariables(d.defaults).vars,
    requiredVariables: requiredList(d.required),
  };
}

/** /templates/:templateId (edit, or read the built-in one) and /templates/new?from=<id> (a copy). A new subject is a fresh editor. */
export function TemplateEditorPage() {
  const { templateId } = useParams();
  const [params] = useSearchParams();
  const from = params.get("from") ?? BUILTIN;
  return <TemplateEditor key={templateId ?? `new:${from}`} templateId={templateId} from={from} />;
}

function TemplateEditor({ templateId, from }: { templateId?: string; from: string }) {
  const isNew = !templateId;
  const source = useTemplate(templateId ?? from);
  const navigate = useNavigate();
  const qc = useQueryClient();

  const [draft, setDraft] = useState<Draft | null>(null);
  const [base, setBase] = useState<string | null>(null); // the saved draft, to tell unsaved changes
  const [etag, setEtag] = useState<string | undefined>();
  const loaded = useRef(false);
  useEffect(() => {
    if (!source.data || loaded.current) return;
    loaded.current = true;
    const d = toDraft(templateInput(source.data));
    if (isNew) {
      d.name = `Copy of ${source.data.name}`;
      if (source.data.builtIn) d.description = "";
      setBase(null);
    } else setBase(JSON.stringify(d));
    setDraft(d);
    setEtag(source.data.etag);
  }, [source.data, isNew]);

  const readOnly = !!source.data?.builtIn && !isNew;
  const dirty = !!draft && (isNew || JSON.stringify(draft) !== base);
  useEffect(() => {
    if (!dirty || readOnly) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty, readOnly]);

  const editor = useRef<CodeEditorHandle>(null);
  const set = (p: Partial<Draft>) => setDraft((d) => (d ? { ...d, ...p } : d));

  const save = useMutation({
    mutationFn: async (d: Draft) => {
      const body = toInput(d);
      if (isNew) return unwrap(await api.POST("/unattend-templates", { body }));
      return unwrap(
        await api.PUT("/unattend-templates/{templateId}", { params: { path: { templateId: templateId! }, header: { "If-Match": etag ?? "*" } }, body }),
      );
    },
    onSuccess: (saved, d) => {
      refreshTemplates();
      qc.setQueryData(["unattend-template", saved.id], saved);
      setBase(JSON.stringify(d));
      setEtag(saved.etag);
      if (isNew) navigate(`/templates/${saved.id}`, { replace: true });
    },
  });

  const back = (
    <Link to="/templates" className="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground">
      <ArrowLeft className="size-4" aria-hidden /> Unattend templates
    </Link>
  );
  if (source.isError) {
    const gone = source.error instanceof ApiError && source.error.status === 404;
    return (
      <div className="max-w-5xl">
        {back}
        <Alert tone="error" title={gone ? "No such template" : "The template couldn’t be loaded"}>
          {gone ? "It may have been deleted." : errorMessage(source.error)}
        </Alert>
      </div>
    );
  }
  if (!draft) return <p className="text-muted-foreground">Loading template…</p>;

  const defaultsBad = parseVariables(draft.defaults).bad;
  const problems = [
    !draft.name.trim() && "Give the template a name.",
    draft.modes.length === 0 && "Choose at least one kind of accounts it handles.",
    defaultsBad.length > 0 && "Each default value needs name=value.",
  ].filter(Boolean) as string[];
  const saveError = save.error;
  const saveLine = templateErrorLine(saveError);

  return (
    <div className="max-w-[110rem]">
      {back}
      <PageHeader
        title={isNew ? "New template" : draft.name || "Template"}
        description={
          readOnly
            ? "The template netdeploy ships. It can’t be changed; make a copy to customise it. Copies don’t pick up later changes to this one."
            : "Changes take effect for deployments armed or started after you save. Ones already armed keep the copy they took."
        }
        actions={
          readOnly ? (
            <Button variant="primary" onClick={() => navigate(`/templates/new?from=${BUILTIN}`)}>
              <Copy aria-hidden /> Make a copy
            </Button>
          ) : (
            <>
              {!isNew && dirty && (
                <Button onClick={() => base && setDraft(JSON.parse(base) as Draft)}>
                  <RotateCcw aria-hidden /> Discard changes
                </Button>
              )}
              {!isNew && !dirty && (
                <Button onClick={() => navigate(`/templates/new?from=${encodeURIComponent(templateId!)}`)}>
                  <Copy aria-hidden /> Copy
                </Button>
              )}
              <Button variant="primary" disabled={!dirty || problems.length > 0 || save.isPending} onClick={() => save.mutate(draft)}>
                <Save aria-hidden /> {save.isPending ? "Saving…" : isNew ? "Create template" : "Save"}
              </Button>
            </>
          )
        }
      />

      {save.isError && (
        <Alert tone="error" className="mb-6" title={isNew ? "The template wasn’t created" : "The template wasn’t saved"}>
          {saveError instanceof ApiError && saveError.status === 412 ? (
            "Someone changed it somewhere else since you opened it. Copy your changes, reload the page and apply them again."
          ) : (
            <>
              {templateError(saveError)}
              {saveError instanceof ApiError && saveError.code === "template_invalid" && saveError.fields.length > 0 && (
                <ul className="mt-2 list-disc space-y-1 pl-5">
                  {saveError.fields.map((f, i) => (
                    <li key={i}>{f.message}</li>
                  ))}
                </ul>
              )}
              {saveLine && (
                <Button size="sm" className="mt-2" onClick={() => editor.current?.goToLine(saveLine)}>
                  Go to line {saveLine}
                </Button>
              )}
            </>
          )}
        </Alert>
      )}
      {!readOnly && problems.length > 0 && dirty && (
        <ul className="mb-6 list-disc space-y-1 pl-5 text-sm text-muted-foreground">
          {problems.map((p) => (
            <li key={p}>{p}</li>
          ))}
        </ul>
      )}

      <div className="grid gap-6 xl:grid-cols-2">
        <Section title="Template">
          <div className="space-y-5">
            <fieldset disabled={readOnly} className="space-y-5">
              <div className="grid gap-5 sm:grid-cols-2">
                <Field label="Name" path="name">
                  {(p) => <Input {...p} maxLength={200} value={draft.name} onChange={(e) => set({ name: e.target.value })} />}
                </Field>
                <Field label="Description">
                  {(p) => <Input {...p} value={draft.description} onChange={(e) => set({ description: e.target.value })} />}
                </Field>
              </div>
              <fieldset>
                <legend className="mb-1.5 text-sm font-medium">Handles profiles with</legend>
                <div className="flex flex-wrap gap-x-6 gap-y-2">
                  {templateModes.map((m) => (
                    <Checkbox
                      key={m}
                      label={modeText[m]}
                      checked={draft.modes.includes(m)}
                      disabled={readOnly}
                      onChange={(on) => set({ modes: on ? [...draft.modes, m] : draft.modes.filter((x) => x !== m) })}
                    />
                  ))}
                  <Checkbox label={modeText.entra} hint="Not yet" checked={false} disabled onChange={() => {}} />
                </div>
                <p className="mt-1.5 text-sm text-muted-foreground">
                  Saving checks each one: local accounts must all be created, and an Active Directory join must be in the file for both join methods.
                </p>
              </fieldset>
              <div className="grid gap-5 sm:grid-cols-2">
                <VariablesField
                  text={draft.defaults}
                  onChange={(defaults) => set({ defaults })}
                  hint="Default values, one name=value per line. A profile’s and then a client’s own variables override them."
                />
                <Field label="Required variables" hint="One per line. Arming refuses a machine whose profile or client doesn’t set them.">
                  {(p) => (
                    <Textarea {...p} className="font-mono" rows={3} value={draft.required} onChange={(e) => set({ required: e.target.value })} placeholder="site" />
                  )}
                </Field>
              </div>
            </fieldset>
            <div>
              <p className="mb-1.5 text-sm font-medium" id="template-content-label">
                Answer file
              </p>
              <CodeEditor
                ref={editor}
                label="Answer file template"
                value={draft.content}
                readOnly={readOnly}
                onChange={(content) => set({ content })}
                className="h-[60vh] min-h-96"
              />
              <p className="mt-1.5 text-sm text-muted-foreground">
                Values from <code className="font-mono">{"{{ … }}"}</code> are escaped for XML; write the template’s own <code className="font-mono">&lt;</code>,{" "}
                <code className="font-mono">&gt;</code> and <code className="font-mono">&amp;</code> as entities. Ctrl+F searches.
              </p>
            </div>
            <Reference />
          </div>
        </Section>

        <Preview draft={toInput(draft)} onGoToTemplateLine={(n) => editor.current?.goToLine(n)} />
      </div>
    </div>
  );
}

/** The live preview: the draft rendered for sample data or a real profile and client, with findings. */
function Preview({ draft, onGoToTemplateLine }: { draft: TemplateInput; onGoToTemplateLine: (n: number) => void }) {
  const profiles = useProfiles();
  const [profileId, setProfileId] = useState("");
  const [clientId, setClientId] = useState("");
  const [mode, setMode] = useState<Mode | "">("");
  const clients = useClients(profileId ? { profileId } : {});
  const output = useRef<CodeEditorHandle>(null);

  const sampleMode = (mode && draft.identityModes.includes(mode) ? mode : draft.identityModes[0]) as Mode | undefined;
  const ctx = useMemo(
    () => (profileId ? { profileId, clientId: clientId || undefined } : { identityMode: sampleMode }),
    [profileId, clientId, sampleMode],
  );
  // Debounce the serialised draft: the object itself is new on every render.
  const json = JSON.stringify(draft);
  const debouncedJson = useDebounced(json, 600);
  const debounced = useMemo(() => JSON.parse(debouncedJson) as TemplateInput, [debouncedJson]);
  const preview = usePreview(debounced, ctx, debounced.identityModes.length > 0 && debounced.content.trim() !== "");
  const errLine = templateErrorLine(preview.error);
  const findings = preview.data?.findings ?? [];
  const stale = preview.isFetching || debouncedJson !== json;

  return (
    <Section
      title="Preview"
      description="What a machine would get, with every secret shown as ********. Updates as you type."
      actions={stale ? <LoaderCircle className="size-4 animate-spin text-muted-foreground" aria-label="Updating" /> : undefined}
    >
      <div className="space-y-5">
        <div className="grid gap-5 sm:grid-cols-2">
          <Field label="Preview with">
            {(p) => (
              <Select
                {...p}
                value={profileId}
                onChange={(e) => {
                  setProfileId(e.target.value);
                  setClientId("");
                }}
              >
                <option value="">Sample data</option>
                {(profiles.data?.items ?? []).map((pr: Schemas["Profile"]) => (
                  <option key={pr.id} value={pr.id}>
                    Profile: {pr.name}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          {profileId ? (
            <Field label="Machine" hint="Its serial, MAC and variables fill the template.">
              {(p) => (
                <Select {...p} value={clientId} onChange={(e) => setClientId(e.target.value)}>
                  <option value="">A sample machine</option>
                  {(clients.data ?? []).map((c) => (
                    <option key={c.id} value={c.id}>
                      {clientName(c)} · {c.mac}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
          ) : (
            <Field label="Accounts">
              {(p) => (
                <Select {...p} value={sampleMode ?? ""} disabled={draft.identityModes.length === 0} onChange={(e) => setMode(e.target.value as Mode)}>
                  {draft.identityModes.map((m) => (
                    <option key={m} value={m}>
                      {modeText[m]}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
          )}
        </div>

        {preview.isError && (
          <Alert tone="error" title="Can’t preview">
            {templateError(preview.error)}
            {errLine && (
              <div>
                <Button size="sm" className="mt-2" onClick={() => onGoToTemplateLine(errLine)}>
                  Go to line {errLine} of the template
                </Button>
              </div>
            )}
          </Alert>
        )}
        {preview.data && !preview.isError && (
          <div>
            <p className="mb-2 text-sm font-medium">
              {preview.data.ok ? (
                <Badge tone={findings.length ? "warning" : "success"}>{findings.length ? "Can be saved, with warnings" : "No problems found"}</Badge>
              ) : (
                <Badge tone="error">Won’t save or deploy as it is</Badge>
              )}
            </p>
            {findings.length > 0 && (
              <ul className="space-y-1.5 text-sm">
                {[...findings]
                  .sort((a, b) => (a.severity === b.severity ? 0 : a.severity === "error" ? -1 : 1))
                  .map((f, i) => (
                    <li key={i} className="flex items-start gap-2">
                      <span className="shrink-0">
                      <Badge tone={f.severity === "error" ? "error" : f.severity === "warning" ? "warning" : "neutral"}>
                        {f.severity === "error" ? "Error" : f.severity === "warning" ? "Warning" : "Note"}
                      </Badge>
                      </span>
                      <span className="min-w-0">
                        {f.message}
                        {f.line != null && (
                          <>
                            {" "}
                            <button type="button" className="text-primary underline-offset-4 hover:underline" onClick={() => output.current?.goToLine(f.line!)}>
                              line {f.line}
                            </button>
                          </>
                        )}
                      </span>
                    </li>
                  ))}
              </ul>
            )}
          </div>
        )}
        <div>
          <p className="mb-1.5 text-sm font-medium">Rendered answer file</p>
          <CodeEditor
            ref={output}
            readOnly
            label="Rendered answer file"
            value={preview.isError ? "" : (preview.data?.content ?? "")}
            className={cn("h-[60vh] min-h-96 transition-opacity", stale && "opacity-70")}
          />
          {draft.identityModes.length === 0 && <p className="mt-1.5 text-sm text-muted-foreground">Choose what the template handles to preview it.</p>}
        </div>
      </div>
    </Section>
  );
}
