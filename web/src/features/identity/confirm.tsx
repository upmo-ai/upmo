import { Button, Group, Modal, Stack, Text } from "@mantine/core";
import type { ReactNode } from "react";

export function ConfirmDialog({
  opened,
  title,
  body,
  action,
  onClose,
  onConfirm,
  busy,
  confirmDisabled,
  children,
}: {
  opened: boolean;
  title: string;
  body: string;
  action: string;
  onClose: () => void;
  onConfirm: () => void;
  busy?: boolean;
  confirmDisabled?: boolean;
  children?: ReactNode;
}) {
  if (!opened) return null;
  return (
    <Modal opened onClose={onClose} title={title} withCloseButton={false}>
      <Stack gap="md">
        <Text>{body}</Text>
        {children}
        <Group gap="xs">
          <Button type="button" disabled={busy || confirmDisabled} onClick={onConfirm}>
            {action}
          </Button>
          <Button type="button" variant="default" onClick={onClose}>
            Cancel
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
