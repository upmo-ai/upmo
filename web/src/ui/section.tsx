import { Stack, Title } from "@mantine/core";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { Button, EmptyState } from "@upmo-ai/components";

import { CaretIcon } from "./icons";

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
      <EmptyState
        title={emptyTitle}
        description={emptyBody}
        icon={<CaretIcon size={24} />}
        action={
          emptyAction ? (
            <Button component={Link} to={emptyAction.to}>
              {emptyAction.label}
            </Button>
          ) : undefined
        }
        firstRun
      />
    </Stack>
  );
}
