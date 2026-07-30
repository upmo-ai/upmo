import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { useState } from "react";

import { ApiProvider, type ApiClient } from "./app/api";
import { createAppRouter, type RouterHistory } from "./app/router";

export function App({
  history,
  client,
}: {
  history?: RouterHistory;
  client?: ApiClient;
} = {}) {
  const [router] = useState(() => createAppRouter(history));
  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: { retry: false, refetchOnWindowFocus: false, staleTime: Infinity },
        },
      }),
  );

  return (
    <ApiProvider client={client}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </ApiProvider>
  );
}
