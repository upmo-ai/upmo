import { Button, Stack, Text, ThemeIcon, Title } from "@mantine/core";
import { Link } from "@tanstack/react-router";
import { IconChevronUp } from "@tabler/icons-react";
import type { ReactNode } from "react";

import "./section.css";

export function EmptyState({
  title,
  body,
  action,
  firstRun = false,
}: {
  title: string;
  body: string;
  action?: { label: string; to: string };
  firstRun?: boolean;
}) {
  return (
    <Stack
      align="center"
      gap="xs"
      py={48}
      px="md"
      className={firstRun ? "empty-state empty-state-first-run" : "empty-state"}
    >
      <ThemeIcon variant="light" size={48} radius="lg" aria-hidden>
        <IconChevronUp size={28} stroke={2} />
      </ThemeIcon>
      <Title order={3}>{title}</Title>
      <Text c="dimmed" maw={320} ta="center">
        {body}
      </Text>
      {action ? (
        <Button component={Link} to={action.to} mt="sm">
          {action.label}
        </Button>
      ) : null}
    </Stack>
  );
}

export function Section({
  title,
  emptyTitle,
  emptyBody,
  emptyAction,
  children,
}: {
  title: string;
  emptyTitle: string;
  emptyBody: string;
  emptyAction?: { label: string; to: string };
  children?: ReactNode;
}) {
  return (
    <Stack gap="md">
      <Title order={1}>{title}</Title>
      {children}
      <EmptyState title={emptyTitle} body={emptyBody} action={emptyAction} firstRun />
    </Stack>
  );
}
