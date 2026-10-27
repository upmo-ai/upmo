import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Wordmark } from "@upmo-ai/components";

import { sessionQueryKey, useApi } from "./api";

export function BrandLink({ className }: { className?: string }) {
  const api = useApi();
  const session = useQuery({
    queryKey: sessionQueryKey(),
    queryFn: () => api.session.get(),
  });
  const loggedIn = session.data?.signIn != null;

  return (
    <Link
      to={loggedIn ? "/" : "/login"}
      className={["wordmark-link", className].filter(Boolean).join(" ")}
      aria-label="Upmo"
    >
      <Wordmark decorative height={28} />
    </Link>
  );
}
