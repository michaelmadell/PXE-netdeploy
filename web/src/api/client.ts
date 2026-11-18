import createClient from "openapi-fetch";
import type { components, paths } from "./schema";
import { queryClient } from "./query";

/**
 * Typed API client. `schema.d.ts` is generated from api/openapi.yaml by
 * `just gen` (pnpm gen:api); never edit it by hand.
 */
export const api = createClient<paths>({ baseUrl: "/api/v1" });

// A 401 means the session ended (idle, signed out elsewhere, password
// changed): re-read it, and the app shows the sign-in page (ADR 0029).
api.use({
  onResponse({ request, response }) {
    if (response.status === 401 && !request.url.endsWith("/auth/session")) {
      void queryClient.invalidateQueries({ queryKey: ["session"] });
    }
  },
});

export type Schemas = components["schemas"];
