import { Button } from "@mantine/core";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";

import { meQueryKey, useApi } from "./api";
import { Wordmark } from "../ui/wordmark";

import "./sign-in.css";

export function SignInPage() {
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const enter = (method: "passkey" | "password") => {
    void api.me.signIn(method).then(async () => {
      await queryClient.invalidateQueries({ queryKey: meQueryKey() });
      await navigate({ to: "/" });
    });
  };

  return (
    <main className="sign-in">
      <Wordmark className="sign-in-wordmark" />
      <div className="sign-in-actions">
        <Button type="button" onClick={() => enter("passkey")}>
          Sign in with a passkey
        </Button>
        <Button type="button" variant="default" onClick={() => enter("password")}>
          Use a password instead
        </Button>
      </div>
    </main>
  );
}
