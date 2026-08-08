import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import { Wordmark } from "../ui/wordmark";
import { meQueryKey, useApi } from "./api";

async function hasLiveSession() {
  try {
    const response = await fetch("/api/session", { credentials: "include" });
    return response.ok;
  } catch {
    return false;
  }
}

export function BrandLink({ className }: { className?: string }) {
  const api = useApi();
  const me = useQuery({ queryKey: meQueryKey(), queryFn: () => api.me.current() });
  const live = useQuery({ queryKey: ["live-session"], queryFn: hasLiveSession });
  const loggedIn = me.data?.signIn != null || live.data === true;

  return (
    <Link to={loggedIn ? "/" : "/login"} className={["wordmark-link", className].filter(Boolean).join(" ")}>
      <Wordmark />
    </Link>
  );
}
