import { useQuery } from "@tanstack/react-query";
import { api, type Schemas } from "@/api/client";
import { queryClient } from "@/api/query";
import { ApiError, errorMessage, unwrap } from "@/lib/problem";

export type Client = Schemas["Client"];
export type ClientInput = Schemas["ClientInput"];
export type ClientFilter = { source?: Schemas["ClientSource"]; profileId?: string };

/** Query keys: ["clients", filter] for lists, ["client", id]. */
export function useClients(filter: ClientFilter = {}) {
  return useQuery({
    queryKey: ["clients", filter],
    queryFn: async () => {
      const all: Client[] = [];
      let cursor: string | undefined;
      do {
        const page = unwrap(await api.GET("/clients", { params: { query: { limit: 200, cursor, ...filter } } }));
        all.push(...page.items);
        cursor = page.nextCursor ?? undefined;
      } while (cursor);
      return all;
    },
  });
}

export function useClient(id: string) {
  return useQuery({
    queryKey: ["client", id],
    queryFn: async () => unwrap(await api.GET("/clients/{clientId}", { params: { path: { clientId: id } } })),
  });
}

/** Same key as the Profiles page. */
export function useProfiles() {
  return useQuery({
    queryKey: ["profiles"],
    queryFn: async () => unwrap(await api.GET("/profiles", { params: { query: { limit: 200 } } })),
  });
}

export function useSecrets(kind: Schemas["SecretKind"]) {
  return useQuery({
    queryKey: ["secrets", kind],
    queryFn: async () => unwrap(await api.GET("/secrets", { params: { query: { kind, limit: 200 } } })),
  });
}

export function refreshClients() {
  for (const key of ["clients", "client", "health"]) void queryClient.invalidateQueries({ queryKey: [key] });
}

/** What to call a client: its hostname, else its serial, else its MAC. */
export const clientName = (c: Pick<Client, "hostname" | "serial" | "mac">) => c.hostname || c.serial || c.mac;

/** The writable part of a client, for a PUT that keeps what a form doesn't show. */
export function clientInput(c: Client): ClientInput {
  return {
    mac: c.mac,
    smbiosUuid: c.smbiosUuid ?? null,
    serial: c.serial ?? null,
    hostname: c.hostname ?? null,
    profileId: c.profileId ?? null,
    variables: c.variables ?? {},
    reservedIp: c.reservedIp ?? null,
    secretRefs: { odjBlob: c.secretRefs?.odjBlob ?? null },
  };
}

const codes: Record<string, string> = {
  mac_taken: "Another client already has this MAC address.",
  reserved_ip_taken: "Another client already has this reserved address.",
  deployment_active: "The client has a deployment armed or running. Cancel it first.",
};
export const clientError = (e: unknown) => (e instanceof ApiError && codes[e.code]) || errorMessage(e);

export const sourceText: Record<Schemas["ClientSource"], string> = {
  manual: "Added by hand",
  discovered: "Discovered",
};
