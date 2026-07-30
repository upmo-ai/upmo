import { Stack, Text, Title } from "@mantine/core";
import type { ReactNode } from "react";

export function EmptyState({ title, body }: { title: string; body: string }) {
  return (
    <Stack align="center" gap="xs" py={48} px="md">
      <Title order={3}>{title}</Title>
      <Text c="dimmed" maw={320} ta="center">
        {body}
      </Text>
    </Stack>
  );
}

export function Section({
  title,
  emptyTitle,
  emptyBody,
  children,
}: {
  title: string;
  emptyTitle: string;
  emptyBody: string;
  children?: ReactNode;
}) {
  return (
    <Stack gap="md">
      <Title order={1}>{title}</Title>
      {children}
      <EmptyState title={emptyTitle} body={emptyBody} />
    </Stack>
  );
}
