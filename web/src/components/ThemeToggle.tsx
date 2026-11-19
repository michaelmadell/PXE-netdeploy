import { Monitor, Moon, Sun, type LucideIcon } from "lucide-react";
import { setTheme, useTheme, type Theme } from "@/lib/theme";
import { cn } from "@/lib/utils";

const options: { value: Theme; label: string; icon: LucideIcon }[] = [
  { value: "light", label: "Light", icon: Sun },
  { value: "dark", label: "Dark", icon: Moon },
  { value: "system", label: "System", icon: Monitor },
];

/** Light / Dark / System, as a row of pressed-state buttons. */
export function ThemeToggle() {
  const theme = useTheme();
  return (
    <div className="flex items-center justify-between gap-2">
      <span id="theme-label" className="text-xs text-muted-foreground">
        Theme
      </span>
      <div role="group" aria-labelledby="theme-label" className="inline-flex rounded-md border border-sidebar-border p-0.5">
        {options.map(({ value, label, icon: Icon }) => (
          <button
            key={value}
            type="button"
            aria-pressed={theme === value}
            aria-label={label}
            title={label}
            onClick={() => setTheme(value)}
            className={cn(
              "rounded-sm p-1.5 text-muted-foreground transition-colors",
              "hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-1",
              theme === value && "bg-sidebar-accent text-foreground",
            )}
          >
            <Icon className="size-3.5" aria-hidden />
          </button>
        ))}
      </div>
    </div>
  );
}
