import { ActionIcon, Button, Group, Stack, Text, Textarea, TextInput, Title, Tooltip } from "@mantine/core";
import { useEffect, useState } from "react";

import { ConfirmDialog } from "../identity/confirm";
import { EmptyState } from "../../ui/section";
import { ShellIcon, type ShellConcept } from "../../ui/icons";

import "./memory.css";

type Fact = {
  id: string;
  claim: string;
  trust: string;
  confirmed_at: string;
};

type Page = {
  profile: string;
  facts: Fact[];
};

function IconAction({
  label,
  concept,
  onClick,
}: {
  label: string;
  concept: Extract<ShellConcept, "action.edit" | "action.delete">;
  onClick: () => void;
}) {
  return (
    <Tooltip label={label} position="bottom" openDelay={400}>
      <ActionIcon aria-label={label} variant="subtle" size={40} onClick={onClick}>
        <ShellIcon concept={concept} />
      </ActionIcon>
    </Tooltip>
  );
}

async function send(path: string, method: string, body?: unknown): Promise<Response> {
  return fetch(path, {
    method,
    credentials: "include",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}

async function read<T>(response: Response): Promise<T> {
  if (response.status === 204) return undefined as T;
  const payload = (await response.json()) as T & { error?: string };
  if (!response.ok) throw new Error(payload.error || "Personalisation could not be saved");
  return payload;
}

function when(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "short", year: "numeric" }).format(date);
}

function source(fact: Fact): string {
  const who = fact.trust === "user" ? "Added by you" : "Learned";
  const day = when(fact.confirmed_at);
  return day === "" ? who : `${who} · ${day}`;
}

export function PersonalisationPage() {
  const [loaded, setLoaded] = useState(false);
  const [message, setMessage] = useState("");
  const [profile, setProfile] = useState("");
  const [editingProfile, setEditingProfile] = useState(false);
  const [profileDraft, setProfileDraft] = useState("");
  const [facts, setFacts] = useState<Fact[]>([]);
  const [draft, setDraft] = useState("");
  const [editId, setEditId] = useState<string | null>(null);
  const [editDraft, setEditDraft] = useState("");
  const [forgetId, setForgetId] = useState<string | null>(null);

  const forget = facts.find((fact) => fact.id === forgetId);

  const load = async () => {
    const page = await read<Page>(await send("/api/personalization", "GET"));
    setProfile(page.profile);
    setFacts(page.facts ?? []);
    setLoaded(true);
  };

  useEffect(() => {
    let live = true;
    void send("/api/personalization", "GET")
      .then((response) => read<Page>(response))
      .then((page) => {
        if (!live) return;
        setProfile(page.profile);
        setFacts(page.facts ?? []);
        setLoaded(true);
      })
      .catch((err: unknown) => {
        if (!live) return;
        setMessage(err instanceof Error ? err.message : "Personalisation could not be loaded");
        setLoaded(true);
      });
    return () => {
      live = false;
    };
  }, []);

  const run = async (work: () => Promise<void>) => {
    setMessage("");
    try {
      await work();
    } catch (err: unknown) {
      setMessage(err instanceof Error ? err.message : "Personalisation could not be saved");
    }
  };

  return (
    <Stack gap="lg" className="memory-page">
      <div>
        <Title order={1}>Personalisation</Title>
        <Text c="dimmed" size="sm" mt="xs">
          What Upmo remembers about you. Facts stay personal. Documents belong in Knowledge.
        </Text>
      </div>
      {message ? (
        <Text role="alert" c="red">
          {message}
        </Text>
      ) : null}

      <Stack gap="sm">
        <Group justify="space-between" align="flex-start" wrap="wrap">
          <Title order={2}>Profile</Title>
          {editingProfile ? null : (
            <IconAction
              label="Edit"
              concept="action.edit"
              onClick={() => {
                setProfileDraft(profile);
                setEditingProfile(true);
              }}
            />
          )}
        </Group>
        <section className="memory-profile" aria-label="Profile text">
          {editingProfile ? (
            <Stack gap="sm">
              <Textarea
                aria-label="Profile"
                minRows={3}
                value={profileDraft}
                onChange={(event) => setProfileDraft(event.currentTarget.value)}
              />
              <Group gap="xs">
                <Button
                  size="compact-sm"
                  onClick={() => {
                    const body = profileDraft.trim();
                    void run(async () => {
                      await read(await send("/api/personalization/profile", "PUT", { body }));
                      setProfile(body);
                      setEditingProfile(false);
                    });
                  }}
                >
                  Save
                </Button>
                <Button
                  size="compact-sm"
                  variant="default"
                  onClick={() => {
                    setEditingProfile(false);
                  }}
                >
                  Cancel
                </Button>
              </Group>
            </Stack>
          ) : (
            <Text>{profile}</Text>
          )}
        </section>
        <Text size="sm" c="dimmed">
          This sits at the start of each chat. Keep it short.
        </Text>
      </Stack>

      <Stack gap="sm">
        <Title order={2}>Memory</Title>
        <form
          className="memory-add"
          onSubmit={(event) => {
            event.preventDefault();
            const claim = draft.trim();
            if (claim === "") return;
            void run(async () => {
              await read(await send("/api/personalization/facts", "POST", { claim }));
              setDraft("");
              await load();
            });
          }}
        >
          <TextInput
            aria-label="Add a fact"
            placeholder="Add a fact"
            value={draft}
            onChange={(event) => setDraft(event.currentTarget.value)}
          />
          <Button type="submit" disabled={draft.trim() === ""}>
            Add
          </Button>
        </form>
        {loaded && facts.length === 0 ? (
          <EmptyState
            title="Nothing remembered yet"
            body="Upmo keeps facts you tell it and lessons from tasks. You can delete any of them."
          />
        ) : (
          <div className="memory-list">
            {facts.map((fact) => (
              <div key={fact.id} className="memory-row">
                {editId === fact.id ? (
                  <Stack gap="sm">
                    <Textarea
                      aria-label="Fact"
                      minRows={2}
                      value={editDraft}
                      onChange={(event) => setEditDraft(event.currentTarget.value)}
                    />
                    <Group gap="xs">
                      <Button
                        size="compact-sm"
                        onClick={() => {
                          const claim = editDraft.trim();
                          if (claim === "") return;
                          void run(async () => {
                            await read(await send(`/api/personalization/facts/${fact.id}`, "PUT", { claim }));
                            setEditId(null);
                            await load();
                          });
                        }}
                      >
                        Save
                      </Button>
                      <Button size="compact-sm" variant="default" onClick={() => setEditId(null)}>
                        Cancel
                      </Button>
                    </Group>
                  </Stack>
                ) : (
                  <Group justify="space-between" align="flex-start" wrap="nowrap" gap="sm">
                    <Stack gap={4}>
                      <Text>{fact.claim}</Text>
                      <Text size="sm" c="dimmed">
                        {source(fact)}
                      </Text>
                    </Stack>
                    <Group gap="xs" wrap="nowrap">
                      <IconAction
                        label="Edit"
                        concept="action.edit"
                        onClick={() => {
                          setEditId(fact.id);
                          setEditDraft(fact.claim);
                        }}
                      />
                      <IconAction label="Forget" concept="action.delete" onClick={() => setForgetId(fact.id)} />
                    </Group>
                  </Group>
                )}
              </div>
            ))}
          </div>
        )}
      </Stack>

      <ConfirmDialog
        opened={forget != null}
        title="Forget this fact?"
        body="Upmo will stop using it. You can add it again later if you still need it."
        action="Forget"
        onClose={() => setForgetId(null)}
        onConfirm={() => {
          if (!forget) return;
          const id = forget.id;
          setForgetId(null);
          void run(async () => {
            await read(await send(`/api/personalization/facts/${id}`, "DELETE"));
            await load();
          });
        }}
      />
    </Stack>
  );
}
