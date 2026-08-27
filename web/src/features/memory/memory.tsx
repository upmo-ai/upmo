import { ActionIcon, Badge, Button, Group, Stack, Text, Textarea, TextInput, Title, Tooltip } from "@mantine/core";
import { useState } from "react";

import { ConfirmDialog } from "../identity/confirm";
import { EmptyState } from "../../ui/section";
import { ShellIcon, type ShellConcept } from "../../ui/icons";
import { sampleFacts, sampleProfile, type Fact } from "./sample";

import "./memory.css";

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

export function PersonalisationPage() {
  const [profile, setProfile] = useState(sampleProfile);
  const [editingProfile, setEditingProfile] = useState(false);
  const [profileDraft, setProfileDraft] = useState(sampleProfile);
  const [facts, setFacts] = useState<Fact[]>(sampleFacts);
  const [draft, setDraft] = useState("");
  const [editId, setEditId] = useState<string | null>(null);
  const [editDraft, setEditDraft] = useState("");
  const [forgetId, setForgetId] = useState<string | null>(null);

  const visible = facts.filter((fact) => !fact.forgotten);
  const forget = facts.find((fact) => fact.id === forgetId);

  const addFact = () => {
    const text = draft.trim();
    if (text === "") return;
    setFacts((current) => [
      {
        id: `fact-${current.length + 1}`,
        text,
        source: "Added by you · today",
        scope: "Personal",
        forgotten: false,
        replaced: false,
      },
      ...current,
    ]);
    setDraft("");
  };

  return (
    <Stack gap="lg" className="memory-page">
      <div>
        <Title order={1}>Personalisation</Title>
        <Text c="dimmed" size="sm" mt="xs">
          What Upmo remembers about you. Facts stay personal. Documents belong in Knowledge.
        </Text>
      </div>

      <Stack gap="sm">
        <Group justify="space-between" align="flex-start" wrap="wrap">
          <Title order={2}>Profile</Title>
          {editingProfile ? null : (
            <IconAction label="Edit" concept="action.edit" onClick={() => setEditingProfile(true)} />
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
                    setProfile(profileDraft.trim() || profile);
                    setEditingProfile(false);
                  }}
                >
                  Save
                </Button>
                <Button
                  size="compact-sm"
                  variant="default"
                  onClick={() => {
                    setProfileDraft(profile);
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
            addFact();
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
        {visible.length === 0 ? (
          <EmptyState
            title="Nothing remembered yet"
            body="Upmo keeps facts you tell it and lessons from tasks. You can delete any of them."
          />
        ) : (
          <div className="memory-list">
            {visible.map((fact) => (
              <div
                key={fact.id}
                className="memory-row"
                data-muted={fact.replaced ? "true" : "false"}
              >
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
                          const text = editDraft.trim();
                          if (text === "") return;
                          setFacts((current) =>
                            current.map((item) =>
                              item.id === fact.id
                                ? { ...item, text, source: "Learned in chat · today", replaced: false }
                                : item,
                            ),
                          );
                          setEditId(null);
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
                      <Text>{fact.text}</Text>
                      <Text size="sm" c="dimmed">
                        {fact.source}
                      </Text>
                      <Group gap="xs">
                        <Badge variant="outline" w="fit-content">
                          {fact.scope}
                        </Badge>
                        {fact.replaced ? (
                          <Badge variant="outline" w="fit-content">
                            Replaced
                          </Badge>
                        ) : null}
                      </Group>
                    </Stack>
                    {fact.replaced ? null : (
                      <Group gap="xs" wrap="nowrap">
                        <IconAction
                          label="Edit"
                          concept="action.edit"
                          onClick={() => {
                            setEditId(fact.id);
                            setEditDraft(fact.text);
                          }}
                        />
                        <IconAction label="Forget" concept="action.delete" onClick={() => setForgetId(fact.id)} />
                      </Group>
                    )}
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
          if (forget) {
            setFacts((current) =>
              current.map((fact) => (fact.id === forget.id ? { ...fact, forgotten: true } : fact)),
            );
          }
          setForgetId(null);
        }}
      />
    </Stack>
  );
}
