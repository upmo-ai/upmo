import { QueryCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { useState } from "react";

import { ApiProvider, BrainError, type ApiClient } from "./app/api";
import { createBrainApi } from "./app/brain-api";
import { createAppRouter, type RouterHistory } from "./app/router";

function isUnauthorized(error: unknown): boolean {
  if (error instanceof BrainError) {
    return error.status === 401 && error.error === "unauthorized";
  }
  if (error && typeof error === "object" && "status" in error && (error as { status: unknown }).status === 401) {
    const errorProp = "error" in error ? (error as { error: unknown }).error : undefined;
    return errorProp === "unauthorized";
  }
  return false;
}

export function App({
  history,
  client,
}: {
  history?: RouterHistory;
  client?: ApiClient;
} = {}) {
  const [api] = useState(() => client ?? createBrainApi());
  const [router] = useState(() => createAppRouter(history));
  const [queryClient] = useState(
    () =>
      new QueryClient({
        queryCache: new QueryCache({
          onError: (error) => {
            if (!isUnauthorized(error)) return;
            const path = router.state.location.pathname;
            if (path === "/login" || path === "/login/link" || path === "/terms" || path === "/privacy") {
              return;
            }
            void router.navigate({ to: "/login" });
          },
        }),
        defaultOptions: {
          queries: {
            retry: false,
            retryOnMount: false,
            refetchOnWindowFocus: false,
            staleTime: Infinity,
          },
        },
      }),
  );

  return (
    <ApiProvider client={api}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} context={{ queryClient, api }} />
      </QueryClientProvider>
    </ApiProvider>
  );
}
