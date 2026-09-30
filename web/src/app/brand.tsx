import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import { CaretIcon } from "../ui/icons";
import { Wordmark } from "../ui/wordmark";
import { sessionQueryKey, useApi } from "./api";

export function BrandLink({ className }: { className?: string }) {
  const api = useApi();
  const session = useQuery({
    queryKey: sessionQueryKey(),
    queryFn: () => api.session.get(),
  });
  const loggedIn = session.data?.signIn != null;

  return (
    <Link to={loggedIn ? "/" : "/login"} className={["wordmark-link", className].filter(Boolean).join(" ")}>
      <span className="shell-brand-icon" aria-hidden="true">
        <CaretIcon size={20} strokeWidth={11} />
      </span>
      <Wordmark className="shell-wordmark" />
    </Link>
  );
}
