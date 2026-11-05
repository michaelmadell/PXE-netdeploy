import createClient from "openapi-fetch";
import type { components, paths } from "./schema";

/**
 * Typed API client. `schema.d.ts` is generated from api/openapi.yaml by
 * `just gen` (pnpm gen:api); never edit it by hand.
 */
export const api = createClient<paths>({ baseUrl: "/api/v1" });

export type Schemas = components["schemas"];
