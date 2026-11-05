import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { StatusBadge } from "@/components/StatusBadge";

export function Overview() {
  const health = useQuery({
    queryKey: ["health"],
    queryFn: async () => {
      const { data, error } = await api.GET("/system/health");
      // A 503 still carries a Health body; show it rather than a generic error.
      if (error) return error as NonNullable<typeof data>;
      return data;
    },
    refetchInterval: 5_000,
  });

  return (
    <section className="max-w-3xl">
      <h1 className="text-xl font-semibold tracking-tight">Overview</h1>

      {health.isPending && <p className="mt-4 text-muted-foreground">Checking server health…</p>}

      {health.isError && (
        <p role="alert" className="mt-4 text-destructive">
          Can’t reach the server. Check that netdeployd is running and listening on the admin address.
        </p>
      )}

      {health.data && (
        <>
          <p className="mt-2 text-muted-foreground">
            Server status: <StatusBadge status={health.data.status} />
          </p>
          <table className="mt-6 w-full text-left text-sm">
            <thead className="text-muted-foreground">
              <tr className="border-b">
                <th scope="col" className="py-2 pr-4 font-medium">Service</th>
                <th scope="col" className="py-2 pr-4 font-medium">Status</th>
                <th scope="col" className="py-2 font-medium">Detail</th>
              </tr>
            </thead>
            <tbody>
              {Object.entries(health.data.services).map(([name, svc]) => (
                <tr key={name} className="border-b last:border-0">
                  <td className="py-2 pr-4 font-medium">{name}</td>
                  <td className="py-2 pr-4">
                    <StatusBadge status={svc.status} />
                  </td>
                  <td className="py-2 text-muted-foreground">{svc.detail ?? ""}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </section>
  );
}
