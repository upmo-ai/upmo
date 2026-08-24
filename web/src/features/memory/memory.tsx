import { Badge, Button, Group, Stack, Text, Title } from "@mantine/core";
import { useState } from "react";

import { ConfirmDialog } from "../identity/confirm";
import { EmptyState } from "../../ui/section";
import { sampleFacts, sampleProfile, type Fact } from "./sample";

import "./memory.css";

export function MemoryPage() {
  const [facts, setFacts] = useState<Fact[]>(sampleFacts);
  const [promoteId, setPromoteId] = useState<string | null>(null);
  const [forgetId, setForgetId] = useState<string | null>(null);
  const visible = facts.filter((fact) => !fact.forgotten);
  const promote = facts.find((fact) => fact.id === promoteId);
  const forget = facts.find((fact) => fact.id === forgetId);

  return (
    <Stack gap="md" className="memory-page">
      <Title order={1}>Memory</Title>
      <Text c="dimmed" size="sm">
        Personal by default. Sample facts until memory exists.
      </Text>
      <section className="memory-profile" aria-labelledby="memory-profile">
        <Title order={2} id="memory-profile">
          Profile
        </Title>
        <Text mt="xs">{sampleProfile}</Text>
      </section>
      {visible.length === 0 ? (
        <EmptyState
          title="Nothing remembered yet"
          body="Upmo keeps facts you tell it and lessons from tasks. You can delete any of them."
        />
      ) : (
        <div className="memory-list">
          {visible.map((fact) => (
            <div key={fact.id} className="memory-row" data-forgotten="false">
              <Group justify="space-between" align="flex-start" wrap="nowrap" gap="sm">
                <Stack gap={4}>
                  <Text>{fact.text}</Text>
                  <Text size="sm" c="dimmed">
                    {fact.source}
                  </Text>
                  <Badge variant="outline" w="fit-content">
                    {fact.scope}
                  </Badge>
                </Stack>
                <Group gap="xs" className="memory-row-actions" wrap="nowrap">
                  {fact.scope === "User" ? (
                    <Button size="compact-sm" variant="subtle" onClick={() => setPromoteId(fact.id)}>
                      Promote
                    </Button>
                  ) : null}
                  <Button size="compact-sm" variant="subtle" onClick={() => setForgetId(fact.id)}>
                    Forget
                  </Button>
                </Group>
              </Group>
            </div>
          ))}
        </div>
      )}
      <ConfirmDialog
        opened={promote != null}
        title="Promote this fact?"
        body="The Finance team can use it after you promote it. It stays a fact you can forget."
        action="Promote"
        onClose={() => setPromoteId(null)}
        onConfirm={() => {
          if (promote) {
            setFacts((current) =>
              current.map((fact) =>
                fact.id === promote.id ? { ...fact, scope: "Team · Finance" } : fact,
              ),
            );
          }
          setPromoteId(null);
        }}
      />
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
