import { Button, Text, Title } from "@mantine/core";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";

import { createPasskeyFromLink } from "./passkeys";
import { Wordmark } from "../ui/wordmark";

import "./sign-in.css";

export function LinkPage() {
  const navigate = useNavigate();
  const [message, setMessage] = useState("");
  const token = window.location.hash.replace(/^#/, "");

  const create = async () => {
    setMessage("");
    try {
      await createPasskeyFromLink(token);
      await navigate({ to: "/" });
    } catch {
      setMessage("This link cannot be used. Ask for a new one.");
    }
  };

  return (
    <main className="sign-in">
      <Wordmark className="sign-in-wordmark" />
      <Title order={1}>Create your passkey</Title>
      <div className="sign-in-actions">
        <Text>This link is the only step. It does not log you in until the passkey exists.</Text>
        <Button type="button" onClick={() => void create()}>
          Create your passkey
        </Button>
        {message ? <Text role="status">{message}</Text> : null}
      </div>
    </main>
  );
}
