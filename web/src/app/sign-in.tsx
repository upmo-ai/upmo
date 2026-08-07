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
  const [password, setPassword] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);

  const logIn = async () => {
    setBusy(true);
    setMessage("");
    try {
      const response = await fetch("/api/sign-in/password", {
        method: "POST",
        credentials: "include",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ email, password }),
      });
      if (response.status === 401) {
        setMessage("Email or password is wrong.");
        return;
      }
      if (!response.ok) {
        setMessage("Try again.");
        return;
      }
      const body = (await response.json()) as { offer_passkey?: boolean };
      await api.me.signIn("password");
      if (!body.offer_passkey) await api.me.dismissPasskeyOffer();
      await queryClient.invalidateQueries({ queryKey: meQueryKey() });
      await navigate({ to: "/" });
    } catch (err) {
      if (err instanceof TypeError) {
        setMessage("Try again.");
        return;
      }
      throw err;
    } finally {
      setBusy(false);
    }
  };

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
        <TextInput
          label="Password"
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={(event) => setPassword(event.currentTarget.value)}
        />
        <Button type="button" onClick={() => void passkey()}>
          Log in with a passkey
        </Button>
        <Button type="button" loading={busy} onClick={() => void logIn()}>
          Log in
        </Button>
        {message ? <Text role="status">{message}</Text> : null}
        <Button type="button" variant="default" onClick={() => void enter()}>
          Use a password instead
        </Button>
      </div>
    </main>
  );
}
