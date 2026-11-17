import { useMutation } from "@tanstack/react-query";
import { Copy, FileCode2, Pencil, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { api } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Alert, Badge, Dialog, PageHeader, td, th } from "@/components/ui/misc";
import { formatDateTime } from "@/lib/format";
import { errorMessage, unwrap } from "@/lib/problem";
import { useProfiles } from "@/pages/clients/data";
import { BUILTIN, modeText, profileTemplateId, refreshTemplates, templateError, useTemplates, type Template } from "./data";

export function TemplatesPage() {
  const templates = useTemplates();
  const profiles = useProfiles();
  const navigate = useNavigate();
  const [deleting, setDeleting] = useState<Template | null>(null);
  const del = useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/unattend-templates/{templateId}", { params: { path: { templateId: id } } })),
    onSuccess: () => {
      refreshTemplates();
      setDeleting(null);
    },
  });

  const usedBy = (t: Template) => (profiles.data?.items ?? []).filter((p) => profileTemplateId(p) === t.id);
  const items = templates.data ?? [];
  const custom = items.filter((t) => !t.builtIn);
  const copy = (t: Template) => navigate(`/templates/new?from=${encodeURIComponent(t.id!)}`);
  const newButton = (
    <Button variant="primary" onClick={() => navigate(`/templates/new?from=${BUILTIN}`)}>
      <Plus aria-hidden /> New template
    </Button>
  );
  const deletingUsers = deleting ? usedBy(deleting) : [];

  return (
    <div className="max-w-5xl">
      <PageHeader
        title="Unattend templates"
        description="The answer file Windows Setup reads on each machine: computer name, locale, accounts and domain join, and anything else you add. Profiles use the built-in template unless you choose another."
        actions={newButton}
      />

      {templates.isPending ? (
        <p className="text-muted-foreground">Loading templates…</p>
      ) : templates.isError ? (
        <Alert tone="error" title="Templates couldn’t be loaded">
          {errorMessage(templates.error)}
        </Alert>
      ) : (
        <>
          <div className="overflow-x-auto rounded-lg border bg-card px-5 py-2">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b">
                  <th scope="col" className={th}>Name</th>
                  <th scope="col" className={th}>Handles</th>
                  <th scope="col" className={th}>Used by</th>
                  <th scope="col" className={th}>Changed</th>
                  <th scope="col" className={`${th} pr-0 text-right`}>Actions</th>
                </tr>
              </thead>
              <tbody>
                {items.map((t) => {
                  const users = usedBy(t);
                  return (
                    <tr key={t.id} className="border-b last:border-0">
                      <td className={td}>
                        <Link to={`/templates/${t.id}`} className="font-medium text-primary underline-offset-4 hover:underline">
                          {t.name}
                        </Link>{" "}
                        {t.builtIn && <Badge>Built-in</Badge>}
                        {t.description && <div className="max-w-prose text-xs text-muted-foreground">{t.description}</div>}
                      </td>
                      <td className={td}>{t.identityModes.map((m) => modeText[m]).join(", ")}</td>
                      <td className={td}>
                        {profiles.isPending ? "…" : users.length === 0 ? <span className="text-muted-foreground">No profiles</span> : users.map((p) => p.name).join(", ")}
                      </td>
                      <td className={td}>{t.builtIn ? <span className="text-muted-foreground">With netdeploy</span> : t.updatedAt ? formatDateTime(t.updatedAt) : "–"}</td>
                      <td className={`${td} pr-0`}>
                        <div className="flex justify-end gap-1">
                          <Button size="sm" variant="ghost" aria-label={`Copy ${t.name}`} onClick={() => copy(t)}>
                            <Copy aria-hidden /> Copy
                          </Button>
                          {!t.builtIn && (
                            <>
                              <Button size="sm" variant="ghost" aria-label={`Edit ${t.name}`} onClick={() => navigate(`/templates/${t.id}`)}>
                                <Pencil aria-hidden /> Edit
                              </Button>
                              <Button size="sm" variant="destructive-ghost" aria-label={`Delete ${t.name}`} onClick={() => { del.reset(); setDeleting(t); }}>
                                <Trash2 aria-hidden /> Delete
                              </Button>
                            </>
                          )}
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
          {custom.length === 0 && (
            <div className="mt-6 rounded-lg border border-dashed px-6 py-10 text-center">
              <FileCode2 className="mx-auto size-6 text-muted-foreground" aria-hidden />
              <p className="mt-2 font-medium">No templates of your own yet</p>
              <p className="mx-auto mt-1 max-w-prose text-sm text-muted-foreground">
                Copy the built-in template to add settings of your own, such as commands to run at first sign-in. Copies don’t pick up later changes to the built-in one.
              </p>
            </div>
          )}
        </>
      )}

      <Dialog
        open={!!deleting}
        onClose={() => setDeleting(null)}
        title="Delete template?"
        footer={
          <>
            <Button onClick={() => setDeleting(null)}>Keep</Button>
            <Button variant="destructive" disabled={del.isPending || deletingUsers.length > 0} onClick={() => deleting && del.mutate(deleting.id!)}>
              {del.isPending ? "Deleting…" : "Delete template"}
            </Button>
          </>
        }
      >
        {deletingUsers.length > 0 ? (
          <p className="text-sm">
            <span className="font-medium">{deleting?.name}</span> is used by {deletingUsers.map((p) => p.name).join(", ")}. Choose another template for{" "}
            {deletingUsers.length === 1 ? "that profile" : "those profiles"} first.
          </p>
        ) : (
          <p className="text-sm">
            <span className="font-medium">{deleting?.name}</span> will be removed. Deployments already armed or running keep their own copy and aren’t affected.
          </p>
        )}
        {del.isError && (
          <Alert tone="error" className="mt-4">
            {templateError(del.error)}
          </Alert>
        )}
      </Dialog>
    </div>
  );
}
