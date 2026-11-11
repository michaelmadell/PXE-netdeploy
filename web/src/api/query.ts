import { QueryClient } from "@tanstack/react-query";

/** Shared so code outside React (finished uploads) can refresh what pages show. */
export const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false } },
});
