import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { queryClient } from "@/api/query";
import { unwrap } from "@/lib/problem";

/** Who is signed in, and whether the first account still has to be made (ADR 0029). */
export function useSession() {
  return useQuery({
    queryKey: ["session"],
    queryFn: async () => unwrap(await api.GET("/auth/session")),
    staleTime: 60_000,
    retry: 2,
  });
}

/** After signing in, out or setting up: start again from a clean cache. */
export function sessionChanged() {
  queryClient.removeQueries({ predicate: (q) => q.queryKey[0] !== "session" });
  void queryClient.invalidateQueries({ queryKey: ["session"] });
}
