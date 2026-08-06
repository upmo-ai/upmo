import { Button, Text, TextInput } from "@mantine/core";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { meQueryKey, useApi } from "./api";
import { abortConditional, conditionalPasskey, signInWithPasskey } from "./passkeys";
import { Wordmark } from "../ui/wordmark";

import "./sign-in.css";

export function SignInPage() {
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [email, setEmail] = useState("");
  const [message, setMessage] = useState("");

  const enter = async () => {
    await api.me.signIn("password");
    await queryClient.invalidateQueries({ queryKey: meQueryKey() });
    await navigate({ to: "/" });
  };

  useEffect(() => {
    let live = true;
    void conditionalPasskey(() => live)
      .then(async (signedIn) => {
        if (!live || !signedIn) return;
        await queryClient.invalidateQueries({ queryKey: meQueryKey() });
        await navigate({ to: "/" });
      })
      .catch(() => {
        // A missing passkey or a cancelled prompt leaves the person on this page.
      });
    return () => {
      live = false;
      abortConditional();
    };
  }, [navigate, queryClient]);

  const passkey = async () => {
    setMessage("");
    try {
      await signInWithPasskey();
      await queryClient.invalidateQueries({ queryKey: meQueryKey() });
      await navigate({ to: "/" });
    } catch {
      setMessage("The passkey prompt was cancelled. Try again.");
    }
  };

  return (
    <main className="sign-in">
      <Wordmark className="sign-in-wordmark" />
      <div className="sign-in-actions">
        <TextInput
          label="Email"
          autoComplete="username webauthn"
          value={email}
          onChange={(event) => setEmail(event.currentTarget.value)}
        />
        <Button type="button" onClick={() => void passkey()}>
          Log in with a passkey
        </Button>
        {message ? <Text role="status">{message}</Text> : null}
        <Button type="button" variant="default" onClick={() => void enter()}>
          Use a password instead
        </Button>
      </div>
    </main>
  );
}
