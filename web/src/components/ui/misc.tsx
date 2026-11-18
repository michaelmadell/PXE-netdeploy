import { CircleAlert, CircleCheck, Info, TriangleAlert, type LucideIcon } from "lucide-react";
import { useEffect, useRef, type ReactNode } from "react";
import { cn } from "@/lib/utils";

/** A titled block of a page: heading, optional description and actions, content. */
export function Section({
  title,
  description,
  actions,
  children,
  className,
}: {
  title: string;
  description?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={cn("rounded-lg border bg-card", className)}>
      <header className="flex flex-wrap items-start justify-between gap-3 border-b px-5 py-4">
        <div className="min-w-0">
          <h2 className="text-base font-semibold tracking-tight">{title}</h2>
          {description && <p className="mt-1 max-w-prose text-sm text-muted-foreground">{description}</p>}
        </div>
        {actions}
      </header>
      <div className="px-5 py-5">{children}</div>
    </section>
  );
}

const tones = {
  info: { icon: Info, cls: "border-border bg-muted/60" },
  success: { icon: CircleCheck, cls: "border-success/40 bg-success/10" },
  warning: { icon: TriangleAlert, cls: "border-warning/50 bg-warning/10" },
  error: { icon: CircleAlert, cls: "border-destructive/40 bg-destructive/10" },
} satisfies Record<string, { icon: LucideIcon; cls: string }>;

/** An inline message. `error` and `warning` are announced at once, the others politely. */
export function Alert({
  tone = "info",
  title,
  children,
  className,
}: {
  tone?: keyof typeof tones;
  title?: string;
  children?: ReactNode;
  className?: string;
}) {
  const { icon: Icon, cls } = tones[tone];
  return (
    <div
      role={tone === "error" || tone === "warning" ? "alert" : "status"}
      className={cn("flex gap-3 rounded-md border px-4 py-3 text-sm", cls, className)}
    >
      <Icon className="mt-0.5 size-4 shrink-0" aria-hidden />
      <div className="min-w-0">
        {title && <p className="font-medium">{title}</p>}
        {children && <div className={cn(title && "mt-0.5 text-muted-foreground")}>{children}</div>}
      </div>
    </div>
  );
}

const badgeTones = {
  neutral: "bg-muted text-foreground",
  success: "bg-success/15 text-foreground",
  warning: "bg-warning/20 text-foreground",
  error: "bg-destructive/15 text-foreground",
} as const;

const dotTones = {
  neutral: "bg-muted-foreground/60",
  success: "bg-success",
  warning: "bg-warning",
  error: "bg-destructive",
} as const;

/** A short state label. The dot adds colour; the text carries the meaning. */
export function Badge({ tone = "neutral", children }: { tone?: keyof typeof badgeTones; children: ReactNode }) {
  return (
    <span className={cn("inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium", badgeTones[tone])}>
      <span className={cn("size-1.5 rounded-full", dotTones[tone])} aria-hidden />
      {children}
    </span>
  );
}

export function Progress({ value, label, className }: { value: number; label: string; className?: string }) {
  const pct = Math.max(0, Math.min(100, value));
  return (
    <div
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(pct)}
      className={cn("h-1.5 w-full overflow-hidden rounded-full bg-muted", className)}
    >
      <div className="h-full rounded-full bg-primary transition-[width] duration-300" style={{ width: `${pct}%` }} />
    </div>
  );
}

/** A modal on the native <dialog>: focus trap, Esc and the backdrop come from the browser. */
export function Dialog({
  open,
  onClose,
  title,
  children,
  footer,
  wide,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  footer?: ReactNode;
  /** For long forms: 48rem instead of 32rem. */
  wide?: boolean;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (open && !d.open) d.showModal();
    if (!open && d.open) d.close();
  }, [open]);
  return (
    <dialog
      ref={ref}
      onClose={onClose}
      onClick={(e) => {
        // A click on the backdrop lands on the dialog element itself.
        if (e.target === ref.current) onClose();
      }}
      className={cn(
        "m-auto rounded-lg border bg-popover p-0 text-popover-foreground shadow-xl",
        "backdrop:bg-black/40",
        wide ? "w-[min(48rem,calc(100vw-2rem))]" : "w-[min(32rem,calc(100vw-2rem))]",
      )}
    >
      {open && (
        <div className="flex max-h-[calc(100dvh-4rem)] flex-col">
          <h2 className="border-b px-5 py-4 text-base font-semibold tracking-tight">{title}</h2>
          <div className="overflow-y-auto px-5 py-5">{children}</div>
          {footer && <div className="flex justify-end gap-2 border-t px-5 py-3">{footer}</div>}
        </div>
      )}
    </dialog>
  );
}

/** Page heading with optional actions on the right. */
export function PageHeader({ title, description, actions }: { title: string; description?: ReactNode; actions?: ReactNode }) {
  return (
    <header className="mb-6 flex flex-wrap items-start justify-between gap-4">
      <div className="min-w-0">
        <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
        {description && <p className="mt-1 max-w-prose text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex shrink-0 gap-2">{actions}</div>}
    </header>
  );
}

export const th = "py-2 pr-4 text-left font-medium text-muted-foreground";
export const td = "py-2.5 pr-4 align-top";

export type TabItem<T extends string> = { id: T; label: string; badge?: ReactNode };

/**
 * A row of tabs (WAI-ARIA tabs pattern: arrow keys, Home and End move
 * between them). Pair with `tabPanelProps` on the panel shown.
 */
export function Tabs<T extends string>({
  tabs,
  value,
  onChange,
  label,
  idPrefix,
  className,
}: {
  tabs: TabItem<T>[];
  value: T;
  onChange: (id: T) => void;
  label: string;
  idPrefix: string;
  className?: string;
}) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const move = (to: number) => {
    const n = (to + tabs.length) % tabs.length;
    onChange(tabs[n]!.id);
    refs.current[n]?.focus();
  };
  return (
    <div role="tablist" aria-label={label} className={cn("flex gap-1 overflow-x-auto shadow-[inset_0_-1px_0_var(--border)]", className)}>
      {tabs.map((t, i) => {
        const selected = t.id === value;
        return (
          <button
            key={t.id}
            ref={(el) => void (refs.current[i] = el)}
            type="button"
            role="tab"
            id={`${idPrefix}-tab-${t.id}`}
            aria-selected={selected}
            aria-controls={`${idPrefix}-panel-${t.id}`}
            tabIndex={selected ? 0 : -1}
            onClick={() => onChange(t.id)}
            onKeyDown={(e) => {
              const k = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: tabs.length - 1 }[e.key];
              if (k === undefined) return;
              e.preventDefault();
              move(k);
            }}
            className={cn(
              "flex shrink-0 items-center gap-2 border-b-2 px-3 py-2 text-sm font-medium whitespace-nowrap transition-colors",
              "focus-visible:outline-2 focus-visible:outline-offset-2",
              selected ? "border-primary text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {t.label}
            {t.badge}
          </button>
        );
      })}
    </div>
  );
}

export const tabPanelProps = (idPrefix: string, id: string) => ({
  role: "tabpanel" as const,
  id: `${idPrefix}-panel-${id}`,
  "aria-labelledby": `${idPrefix}-tab-${id}`,
  tabIndex: 0,
  className: "rounded-md focus-visible:outline-2 focus-visible:outline-offset-4",
});
