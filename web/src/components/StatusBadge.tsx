import { cn } from "@/lib/utils";

const styles = {
  ok: "bg-success",
  degraded: "bg-warning",
  disabled: "bg-muted-foreground/50",
  error: "bg-destructive",
  down: "bg-destructive",
} as const;

export type Status = keyof typeof styles;

/** Status shown as a dot plus text, so meaning never depends on colour alone. */
export function StatusBadge({ status }: { status: Status }) {
  return (
    <span className="inline-flex items-center gap-2 text-sm">
      <span className={cn("size-2 rounded-full", styles[status])} aria-hidden />
      {status}
    </span>
  );
}
