import { QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router";
import { queryClient } from "./api/query";
import { App } from "./App";
import "./index.css";
import { initTheme } from "./lib/theme";

initTheme();

// A page still open from an older build asks for chunks the server no
// longer has: reload once to get the current build (not in a loop).
window.addEventListener("vite:preloadError", (e) => {
  try {
    const last = Number(sessionStorage.getItem("nd-reloaded-at") ?? 0);
    if (Date.now() - last < 10_000) return;
    sessionStorage.setItem("nd-reloaded-at", String(Date.now()));
  } catch {
    return;
  }
  e.preventDefault();
  window.location.reload();
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
);
