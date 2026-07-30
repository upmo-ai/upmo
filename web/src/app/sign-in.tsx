import { Button } from "@mantine/core";
import { useNavigate } from "@tanstack/react-router";

import { Wordmark } from "../ui/wordmark";

import "./sign-in.css";

export function SignInPage() {
  const navigate = useNavigate();
  const enter = () => navigate({ to: "/" });

  return (
    <main className="sign-in">
      <Wordmark className="sign-in-wordmark" />
      <div className="sign-in-actions">
        <Button onClick={enter}>Sign in with a passkey</Button>
        <Button variant="default" onClick={enter}>
          Use a password instead
        </Button>
      </div>
    </main>
  );
}
