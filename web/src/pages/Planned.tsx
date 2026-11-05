export function Planned({ title, summary }: { title: string; summary: string }) {
  return (
    <section className="max-w-prose">
      <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
      <p className="mt-2 text-muted-foreground">{summary}</p>
      <p className="mt-6 text-sm text-muted-foreground">This page is not built yet.</p>
    </section>
  );
}
