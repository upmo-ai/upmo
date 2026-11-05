import { PasswordInput } from "@mantine/core";
import { IconKey } from "@tabler/icons-react";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useCallback, useEffect, useRef, useState } from "react";
import { Button, Input, StatusAlert } from "@upmo-ai/components";

import { LoginAgreement } from "./agreement";
import { useApi } from "./api";
import { BrandLink } from "./brand";
import { abortConditional, conditionalPasskey, conditionalPending, signInWithPasskey } from "./passkeys";

import "./sign-in.css";

function tryAgain(response: Response): string {
  if (response.status !== 429) return "Try again.";
  const seconds = Number(response.headers.get("Retry-After"));
  if (!Number.isInteger(seconds) || seconds < 1) return "Try again.";
  return `Try again in ${seconds} ${seconds === 1 ? "second" : "seconds"}.`;
}

export function SignInPage() {
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const liveRef = useRef(true);

  const armConditional = useCallback(() => {
    void conditionalPasskey(() => liveRef.current)
      .then(async (signedIn) => {
        if (!liveRef.current || !signedIn) return;
        queryClient.clear();
        await navigate({ to: "/" });
      })
      .catch(() => {
        // A missing passkey or a cancelled prompt leaves the person on this page.
      });
  }, [navigate, queryClient]);

  // Each options request spends a token from the address throttle, so a live
  // autofill request is kept rather than replaced.
  const rearmConditional = useCallback(() => {
    if (!conditionalPending()) armConditional();
  }, [armConditional]);

  useEffect(() => {
    liveRef.current = true;
    armConditional();
    return () => {
      liveRef.current = false;
      abortConditional();
    };
  }, [armConditional]);

  const logIn = async () => {
    if (busy) return;
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
        rearmConditional();
        return;
      }
      if (!response.ok) {
        setMessage(tryAgain(response));
        rearmConditional();
        return;
      }
      const body = (await response.json()) as { offer_passkey?: boolean };
      await api.me.signIn("password");
      if (!body.offer_passkey) await api.me.dismissPasskeyOffer();
      queryClient.clear();
      await navigate({ to: "/" });
    } catch (err) {
      if (err instanceof TypeError) {
        setMessage("Try again.");
        rearmConditional();
        return;
      }
      throw err;
    } finally {
      setBusy(false);
    }
  };

  const passkey = async () => {
    if (busy) return;
    setBusy(true);
    setMessage("");
    try {
      await signInWithPasskey();
      queryClient.clear();
      await navigate({ to: "/" });
    } catch {
      setMessage("The passkey prompt was cancelled. Try again.");
      rearmConditional();
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <a className="shell-skip" href="#content">
        Skip to content
      </a>
      <main id="content" className="sign-in">
      <BrandLink className="sign-in-wordmark" />
      <form
        className="sign-in-actions"
        onSubmit={(event) => {
          event.preventDefault();
          void logIn();
        }}
      >
        <Input
          label="Email"
          autoComplete="username webauthn"
          value={email}
          onChange={(event) => setEmail(event.currentTarget.value)}
        />
        <PasswordInput
          label="Password"
          autoComplete="current-password"
          value={password}
          onChange={(event) => setPassword(event.currentTarget.value)}
        />
        <Button type="submit" loading={busy}>
          Log in
        </Button>
        <Button
          type="button"
          variant="outline"
          leftSection={<IconKey size={20} stroke={1.75} aria-hidden />}
          onClick={() => void passkey()}
        >
          Log in with a passkey
        </Button>
        {message ? (
          <StatusAlert tone="critical">
            {message}
          </StatusAlert>
        ) : null}
        <LoginAgreement />
      </form>
    </main>
    </>
  );
}
