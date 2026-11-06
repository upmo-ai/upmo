import { Group, Stack } from "@mantine/core";
import { useState } from "react";
import { Button, Dialog, Input } from "@upmo-ai/components";

function NameForm({
  initial,
  action,
  busy,
  error,
  onSubmit,
  onClose,
}: {
  initial: string;
  action: string;
  busy: boolean;
  error: string;
  onSubmit: (name: string) => void;
  onClose: () => void;
}) {
  const [name, setName] = useState(initial);
  const blank = name.trim() === "";

  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        if (!blank) onSubmit(name.trim());
      }}
    >
      <Stack gap="md">
        <Input
          label="Folder name"
          data-autofocus
          value={name}
          error={error || undefined}
          onChange={(event) => setName(event.currentTarget.value)}
        />
        <Group gap="xs" justify="flex-end">
          <Button type="button" variant="default" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" loading={busy} disabled={blank}>
            {action}
          </Button>
        </Group>
      </Stack>
    </form>
  );
}

/** Asks for a folder name. The form is mounted per open, so it starts fresh each time. */
export function FolderDialog({
  opened,
  title,
  action,
  initial = "",
  busy,
  error,
  onSubmit,
  onClose,
}: {
  opened: boolean;
  title: string;
  action: string;
  initial?: string;
  busy: boolean;
  error: string;
  onSubmit: (name: string) => void;
  onClose: () => void;
}) {
  return (
    <Dialog opened={opened} onClose={onClose} title={title} withCloseButton={false}>
      <NameForm
        initial={initial}
        action={action}
        busy={busy}
        error={error}
        onSubmit={onSubmit}
        onClose={onClose}
      />
    </Dialog>
  );
}
