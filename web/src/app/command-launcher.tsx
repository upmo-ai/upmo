import { Button, Modal, Stack, Text, UnstyledButton } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { Link } from "@tanstack/react-router";
import { IconSearch } from "@tabler/icons-react";
import { useEffect } from "react";

const pages = [
  { to: "/", label: "Home" },
  { to: "/chat", label: "Chat" },
  { to: "/tasks", label: "Tasks" },
  { to: "/automations", label: "Automations" },
  { to: "/agents", label: "Agents" },
  { to: "/memory", label: "Memory" },
  { to: "/knowledge", label: "Knowledge" },
  { to: "/usage", label: "Usage" },
  { to: "/settings", label: "Settings" },
  { to: "/settings/members", label: "Members" },
  { to: "/settings/tokens", label: "API tokens" },
  { to: "/settings/connectors", label: "Connectors" },
] as const;

export function CommandLauncher() {
  const [opened, { open, close, toggle }] = useDisclosure(false);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        toggle();
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [toggle]);

  return (
    <>
      <UnstyledButton
        className="shell-launcher"
        aria-label="Search"
        onClick={open}
      >
        <IconSearch size={18} stroke={1.75} aria-hidden />
        <Text span c="dimmed" size="sm">
          Search tasks, chats and knowledge
        </Text>
        <Text span className="shell-launcher-kbd" aria-hidden>
          ⌘K
        </Text>
      </UnstyledButton>
      <Modal opened={opened} onClose={close} title="Go to" size="sm">
        <Stack gap="xs">
          {pages.map((item) => (
            <Button
              key={item.to}
              component={Link}
              to={item.to}
              variant="subtle"
              justify="flex-start"
              onClick={close}
            >
              {item.label}
            </Button>
          ))}
        </Stack>
      </Modal>
    </>
  );
}
