import { useSyncExternalStore } from "react";

/**
 * Light, dark, or the OS preference. shadcn/ui styles hang off the .dark
 * class on <html>; color-scheme makes native controls and scrollbars match.
 * index.html applies the stored choice before first paint, so a dark theme
 * doesn't flash light; this module keeps it in step afterwards.
 */
export type Theme = "light" | "dark" | "system";

const KEY = "theme";
const media = window.matchMedia("(prefers-color-scheme: dark)");
const listeners = new Set<() => void>();
let current: Theme = read();

function read(): Theme {
  try {
    const v = localStorage.getItem(KEY);
    if (v === "light" || v === "dark") return v;
  } catch {
    // Storage blocked (private mode, policy): follow the OS.
  }
  return "system";
}

function apply() {
  const dark = current === "dark" || (current === "system" && media.matches);
  const root = document.documentElement;
  root.classList.toggle("dark", dark);
  root.style.colorScheme = dark ? "dark" : "light";
}

function emit() {
  for (const l of listeners) l();
}

/** Call once at start-up. */
export function initTheme() {
  apply();
  // "System" follows the OS when it switches (e.g. at sunset).
  media.addEventListener("change", () => current === "system" && apply());
  // Another tab changed it.
  window.addEventListener("storage", (e) => {
    if (e.key !== KEY && e.key !== null) return;
    current = read();
    apply();
    emit();
  });
}

export function setTheme(t: Theme) {
  current = t;
  try {
    if (t === "system") localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, t);
  } catch {
    // Not remembered, but still applied for this page.
  }
  apply();
  emit();
}

export function useTheme(): Theme {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => current,
  );
}
