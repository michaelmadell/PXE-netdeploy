import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { api, type Schemas } from "@/api/client";
import { Select } from "@/components/ui/form";
import { Alert, Badge, td, th } from "@/components/ui/misc";
import { formatDateTime } from "@/lib/format";
import { errorMessage, unwrap } from "@/lib/problem";
import { clientName, useClients } from "./data";

type Lease = Schemas["DhcpLease"];

const archText: Record<NonNullable<Lease["arch"]>, string> = {
  bios: "BIOS",
  "uefi-x64": "UEFI x64",
  "uefi-x64-http": "UEFI x64 HTTP Boot",
  "uefi-arm64": "UEFI ARM64",
  "uefi-arm64-http": "UEFI ARM64 HTTP Boot",
};

/** Addresses this server's full-mode DHCP has handed out. Read-only. */
export function LeasesPanel({ iface, setIface }: { iface: string; setIface: (name: string) => void }) {
  const leases = useQuery({
    queryKey: ["dhcp-leases"],
    queryFn: async () => unwrap(await api.GET("/dhcp/leases", { params: { query: { limit: 200 } } })),
    refetchInterval: 30_000,
  });
  const clients = useClients({});
  const byId = new Map((clients.data ?? []).map((c) => [c.id!, c]));
  const all = leases.data?.items ?? [];
  const names = [...new Set(all.map((l) => l.interface))].sort();
  const items = iface ? all.filter((l) => l.interface === iface) : all;

  if (leases.isPending) return <p className="text-muted-foreground">Loading leases…</p>;
  if (leases.isError)
    return (
      <Alert tone="error" title="Leases couldn’t be loaded">
        {errorMessage(leases.error)}
      </Alert>
    );
  return (
    <>
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <label className="flex items-center gap-2 text-sm">
          <span className="text-muted-foreground">Interface</span>
          <Select className="w-64" value={iface} onChange={(e) => setIface(e.target.value)}>
            <option value="">All</option>
            {names.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
            {iface && !names.includes(iface) && <option value={iface}>{iface}</option>}
          </Select>
        </label>
        <span className="text-sm text-muted-foreground">Current leases, refreshed every 30 seconds.</span>
      </div>
      {items.length === 0 ? (
        <div className="rounded-lg border border-dashed px-6 py-12 text-center">
          <p className="font-medium">No leases{iface ? ` on ${iface}` : ""}</p>
          <p className="mx-auto mt-1 max-w-prose text-sm text-muted-foreground">
            Only interfaces with full DHCP hand out addresses. In proxy mode your own DHCP server keeps the leases.
          </p>
        </div>
      ) : (
        <div className="overflow-x-auto rounded-lg border bg-card px-5 py-2">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b">
                <th scope="col" className={th}>Address</th>
                <th scope="col" className={th}>Machine</th>
                <th scope="col" className={th}>Firmware</th>
                <th scope="col" className={th}>Interface</th>
                <th scope="col" className={`${th} pr-0`}>Expires</th>
              </tr>
            </thead>
            <tbody>
              {items.map((l) => {
                const c = l.clientId ? byId.get(l.clientId) : undefined;
                return (
                  <tr key={`${l.interface}/${l.ip}`} className="border-b last:border-0">
                    <td className={`${td} font-mono`}>
                      {l.ip}
                      {l.reserved && (
                        <span className="ml-2 font-sans">
                          <Badge>Reserved</Badge>
                        </span>
                      )}
                    </td>
                    <td className={td}>
                      {l.clientId ? (
                        <Link to={`/clients/${l.clientId}`} className="font-medium text-primary underline-offset-4 hover:underline">
                          {c ? clientName(c) : l.hostname || l.mac}
                        </Link>
                      ) : (
                        <span>{l.hostname || <span className="text-muted-foreground">Unknown machine</span>}</span>
                      )}
                      <div className="font-mono text-xs text-muted-foreground">{l.mac}</div>
                    </td>
                    <td className={td}>{l.arch ? archText[l.arch] : <span className="text-muted-foreground">–</span>}</td>
                    <td className={td}>{l.interface}</td>
                    <td className={`${td} pr-0`}>{formatDateTime(l.expiresAt)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {leases.data.nextCursor && <p className="py-2 text-xs text-muted-foreground">Showing the first 200.</p>}
        </div>
      )}
    </>
  );
}
