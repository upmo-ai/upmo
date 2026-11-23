import { Group, Stack, Text, Title, VisuallyHidden } from "@mantine/core";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState, type ChangeEvent } from "react";
import {
  Button,
  ConfirmDialog,
  DataTable,
  EmptyState,
  IconButton,
  Input,
  PageHeader,
  StatusAlert,
  Textarea,
  Wordmark,
  type DataTableColumn,
} from "@upmo-ai/components";

import { BrainError, membersQueryKey, sessionQueryKey, useApi } from "../../app/api";
import { CaretIcon, ShellIcon, type ShellConcept } from "../../ui/icons";

import "./memory.css";

const MAX_AVATAR_BYTES = 50 * 1024 * 1024;
const ALLOWED_AVATAR_TYPES = new Set(["image/png", "image/jpeg", "image/gif", "image/webp"]);

function avatarErrorMessage(err: unknown): string {
  if (err instanceof BrainError) {
    if (err.status === 413) return "Avatar must be 50 MB or smaller.";
    if (err.status === 415) return "Avatar must be a PNG, JPEG, GIF, or WebP image.";
    if (err.status === 422) return err.error || err.message || "Avatar image could not be processed.";
    if (err.status === 429) return "Slow down. Try again in a moment.";
  }
  return err instanceof Error ? err.message : "Avatar could not be updated.";
}

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
  disabled,
  onClick,
}: {
  label: string;
  concept: Extract<ShellConcept, "action.edit" | "action.delete">;
  disabled?: boolean;
  onClick: () => void;
}) {
  return (
    <IconButton aria-label={label} variant="subtle" size={40} disabled={disabled} onClick={onClick}>
      <ShellIcon concept={concept} />
    </IconButton>
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
  const api = useApi();
  const queryClient = useQueryClient();
  const session = useQuery({
    queryKey: sessionQueryKey(),
    queryFn: () => api.session.get(),
  });
  const avatarUrl = session.data?.avatarUrl ?? null;
  const [avatarBusy, setAvatarBusy] = useState(false);
  const [avatarError, setAvatarError] = useState("");
  const [failedAvatarUrl, setFailedAvatarUrl] = useState<string | null>(null);
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const showAvatarImage = Boolean(avatarUrl) && failedAvatarUrl !== avatarUrl;

  const [loaded, setLoaded] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);
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
    setLoadFailed(false);
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
        setLoadFailed(true);
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

  const refreshIdentity = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: sessionQueryKey() }),
      queryClient.invalidateQueries({ queryKey: membersQueryKey() }),
    ]);
  };

  const handleAvatarChange = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.currentTarget.files?.[0];
    event.currentTarget.value = "";
    if (!file) return;
    setAvatarError("");
    if (file.size > MAX_AVATAR_BYTES) {
      setAvatarError("Avatar must be 50 MB or smaller.");
      return;
    }
    if (!ALLOWED_AVATAR_TYPES.has(file.type)) {
      setAvatarError("Avatar must be a PNG, JPEG, GIF, or WebP image.");
      return;
    }
    setAvatarBusy(true);
    try {
      await api.me.uploadAvatar(file);
      setFailedAvatarUrl(null);
      await refreshIdentity();
    } catch (err: unknown) {
      setAvatarError(avatarErrorMessage(err));
    } finally {
      setAvatarBusy(false);
    }
  };

  const handleRemoveAvatar = async () => {
    setAvatarError("");
    setAvatarBusy(true);
    try {
      await api.me.removeAvatar();
      setFailedAvatarUrl(null);
      await refreshIdentity();
    } catch (err: unknown) {
      setAvatarError(avatarErrorMessage(err));
    } finally {
      setAvatarBusy(false);
    }
  };

  const columns: DataTableColumn<Fact>[] = [
    {
      key: "claim",
      header: "Fact",
      render: (fact) =>
        editId === fact.id ? (
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
          <Text>{fact.claim}</Text>
        ),
    },
    {
      key: "source",
      header: "Source",
      render: (fact) => (
        <Text size="sm" c="dimmed">
          {source(fact)}
        </Text>
      ),
    },
    {
      key: "actions",
      header: <VisuallyHidden>Actions</VisuallyHidden>,
      render: (fact) =>
        editId === fact.id ? null : (
          <Group gap="xs" wrap="nowrap" justify="flex-end">
            <IconAction
              label="Edit fact"
              concept="action.edit"
              onClick={() => {
                setEditId(fact.id);
                setEditDraft(fact.claim);
              }}
            />
            <IconAction label="Forget fact" concept="action.delete" onClick={() => setForgetId(fact.id)} />
          </Group>
        ),
    },
  ];

  return (
    <Stack gap="lg" className="memory-page">
      <PageHeader
        title="Personalisation"
        description="What Upmo remembers about you. Facts stay personal. Documents belong in Knowledge."
      />
      {message ? <StatusAlert tone="critical">{message}</StatusAlert> : null}

      <Stack gap="sm">
        <Group justify="space-between" align="flex-start" wrap="wrap">
          <Title order={2}>Profile</Title>
          {editingProfile ? null : (
            <IconAction
              label="Edit profile"
              concept="action.edit"
              disabled={loadFailed}
              onClick={() => {
                setProfileDraft(profile);
                setEditingProfile(true);
              }}
            />
          )}
        </Group>
        <section className="memory-avatar-card" data-testid="profile-avatar-card" aria-label="Profile photo">
          <div className="memory-avatar-preview">
            {showAvatarImage ? (
              <img
                className="memory-avatar-img"
                src={avatarUrl!}
                alt="Your avatar"
                onError={() => setFailedAvatarUrl(avatarUrl)}
              />
            ) : (
              <span className="memory-avatar-fallback">
                <Wordmark compact height={20} label="Default avatar" />
              </span>
            )}
          </div>
          <div className="memory-avatar-controls">
            <input
              ref={fileInputRef}
              type="file"
              accept="image/png,image/jpeg,image/gif,image/webp"
              className="memory-avatar-input"
              aria-label="Upload avatar"
              onChange={(event) => void handleAvatarChange(event)}
            />
            <Group gap="xs" wrap="wrap">
              <Button
                size="compact-sm"
                variant="default"
                disabled={avatarBusy}
                onClick={() => fileInputRef.current?.click()}
              >
                {avatarUrl ? "Change photo" : "Upload photo"}
              </Button>
              {avatarUrl ? (
                <Button
                  size="compact-sm"
                  variant="default"
                  disabled={avatarBusy}
                  onClick={() => void handleRemoveAvatar()}
                >
                  Remove photo
                </Button>
              ) : null}
            </Group>
            <Text size="sm" c="dimmed">
              PNG, JPEG, GIF, or WebP. Up to 50 MB.
            </Text>
          </div>
        </section>
        {avatarError ? <StatusAlert tone="critical">{avatarError}</StatusAlert> : null}
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
          <Input
            aria-label="Add a fact"
            placeholder="Add a fact"
            value={draft}
            onChange={(event) => setDraft(event.currentTarget.value)}
          />
          <Button type="submit" disabled={draft.trim() === ""}>
            Add
          </Button>
        </form>
        {loaded && !loadFailed && facts.length === 0 ? (
          <EmptyState
            title="Nothing remembered yet"
            description="Upmo keeps facts you tell it and lessons from tasks. You can delete any of them."
            icon={<CaretIcon size={24} />}
          />
        ) : null}
        {facts.length > 0 ? (
          <DataTable
            aria-label="Remembered facts"
            minWidth={560}
            columns={columns}
            rows={facts}
            rowKey={(fact) => fact.id}
          />
        ) : null}
      </Stack>

      <ConfirmDialog
        opened={forget != null}
        title="Forget this fact?"
        body="Upmo will stop using it. You can add it again later if you still need it."
        action="Forget fact"
        destructive
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
