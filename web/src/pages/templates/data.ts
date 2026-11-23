import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { api, type Schemas } from "@/api/client";
import { queryClient } from "@/api/query";
import { ApiError, errorMessage, unwrap } from "@/lib/problem";

export type Template = Schemas["UnattendTemplate"];
export type TemplateInput = Schemas["UnattendTemplateInput"];
export type Mode = Schemas["IdentityMode"];
export type RenderResult = Schemas["UnattendRenderResult"];

export const BUILTIN = "builtin";
export const BUILTIN_AUTOINSTALL = "builtin-autoinstall";
export type Format = Schemas["UnattendFormat"];
export const formatOf = (t: Pick<TemplateInput, "format">): Format => t.format ?? "unattend";

/** Identity modes in words; Entra isn't supported by templates yet. */
export const modeText: Record<Mode, string> = {
  none: "No accounts",
  local: "Local accounts",
  ad: "Active Directory",
  entra: "Microsoft Entra",
};
export const templateModes: Mode[] = ["none", "local", "ad"];

/** Query keys: ["unattend-templates"] for the list, ["unattend-template", id]. No server events: refreshed after our own changes. */
export function useTemplates() {
  return useQuery({
    queryKey: ["unattend-templates"],
    queryFn: async () => {
      const all: Template[] = [];
      let cursor: string | undefined;
      do {
        const page = unwrap(await api.GET("/unattend-templates", { params: { query: { limit: 200, cursor } } }));
        all.push(...page.items);
        cursor = page.nextCursor ?? undefined;
      } while (cursor);
      return all;
    },
  });
}

export function useTemplate(id: string | undefined) {
  return useQuery({
    queryKey: ["unattend-template", id],
    enabled: !!id,
    queryFn: async () => unwrap(await api.GET("/unattend-templates/{templateId}", { params: { path: { templateId: id! } } })),
  });
}

export function refreshTemplates() {
  for (const key of ["unattend-templates", "unattend-template"]) void queryClient.invalidateQueries({ queryKey: [key] });
}

/** The template a profile uses: its id, or its kind's built-in one. */
export const profileTemplateId = (p: Pick<Schemas["Profile"], "unattendTemplateId" | "kind">) =>
  p.unattendTemplateId || (p.kind === "linux" ? BUILTIN_AUTOINSTALL : BUILTIN);

/** The writable part of a template. */
export function templateInput(t: Template): TemplateInput {
  return {
    name: t.name,
    description: t.description ?? "",
    format: formatOf(t),
    content: t.content,
    identityModes: t.identityModes,
    variableDefaults: t.variableDefaults ?? {},
    requiredVariables: t.requiredVariables ?? [],
  };
}

const codes: Record<string, string> = {
  built_in: "The built-in templates can’t be changed. Make a copy and edit that.",
  format_changed: "A template can’t switch between unattend and autoinstall.",
  not_for_format: "Autoinstall templates don’t handle Windows account modes.",
  identity_modes_required: "Choose at least one kind of accounts it handles.",
  template_format_mismatch: "That template is for the other kind of profile.",
  template_in_use: "Profiles still use this template.",
  template_syntax: "The template doesn’t parse.",
  template_render: "The template fails when it’s filled in.",
  template_invalid: "The template is missing things the installer needs.",
  identity_mode_unsupported: "The template doesn’t handle that profile’s accounts.",
};
/** A message for a failed template call: the code in words, then the server's detail. */
export function templateError(e: unknown): string {
  if (!(e instanceof ApiError) || !codes[e.code]) return errorMessage(e);
  // template_invalid's detail only says to see the list, which the page shows.
  if (e.code === "template_invalid") return codes[e.code]!;
  // "template render: template: unattend:75:29: executing …" → "Line 75: executing …"
  const detail = e.message
    .replace(/^template (render|syntax): /, "")
    .replace(/^template: (unattend|user-data):(\d+)(:\d+)?: /, "Line $2: ")
    .replace(/ \(sample [^)]*\)$/, "");
  return detail && detail !== codes[e.code] ? `${codes[e.code]} ${detail}` : codes[e.code]!;
}

/** The line a Go template error names ("template: unattend:12:5: …"). */
export function templateErrorLine(e: unknown): number | undefined {
  const m = e instanceof Error ? /(?:unattend|user-data):(\d+)/.exec(e.message) : null;
  return m ? Number(m[1]) : undefined;
}

export function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setV(value), ms);
    return () => clearTimeout(id);
  }, [value, ms]);
  return v;
}

export type PreviewContext = { profileId?: string; clientId?: string; identityMode?: Mode };

/** Renders a draft (saved or not) with secrets as ********, re-run as the draft changes (debounced by the caller). */
export function usePreview(draft: TemplateInput, ctx: PreviewContext, enabled: boolean) {
  return useQuery({
    queryKey: ["template-preview", draft, ctx],
    enabled,
    placeholderData: keepPreviousData,
    retry: false,
    queryFn: async () =>
      unwrap(
        await api.POST("/unattend-templates/render", {
          body: {
            template: draft,
            profileId: ctx.profileId ?? null,
            clientId: ctx.clientId ?? null,
            ...(ctx.profileId ? {} : { identityMode: ctx.identityMode }),
          },
        }),
      ),
  });
}
