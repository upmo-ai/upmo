import { UnstyledButton } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { Link, useNavigate } from "@tanstack/react-router";
import {
  IconBrain,
  IconCalendar,
  IconChartBar,
  IconChecklist,
  IconHome,
  IconKey,
  IconLock,
  IconMessage,
  IconPlug,
  IconRobot,
  IconSearch,
  IconServer,
  IconSettings,
  IconShieldLock,
  IconUserCheck,
  IconUsers,
} from "@tabler/icons-react";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Dialog, Kbd, KbdGroup } from "@upmo-ai/components";

interface PaletteItem {
  to: string;
  label: string;
  group: "Pages" | "Workspace" | "Management";
  icon: typeof IconHome;
  meta: string;
}

const items: PaletteItem[] = [
  { to: "/", label: "Home", group: "Pages", icon: IconHome, meta: "Go to" },
  { to: "/chat", label: "Chat", group: "Pages", icon: IconMessage, meta: "Go to" },
  { to: "/tasks", label: "Tasks", group: "Pages", icon: IconChecklist, meta: "Go to" },
  { to: "/automations", label: "Automations", group: "Pages", icon: IconCalendar, meta: "Go to" },
  { to: "/agents", label: "Agents", group: "Pages", icon: IconRobot, meta: "Go to" },
  { to: "/knowledge", label: "Knowledge", group: "Workspace", icon: IconBrain, meta: "Go to" },
  { to: "/usage", label: "Usage", group: "Workspace", icon: IconChartBar, meta: "Go to" },
  { to: "/settings/providers", label: "Providers", group: "Workspace", icon: IconServer, meta: "Go to" },
  { to: "/connectors", label: "Connectors", group: "Workspace", icon: IconPlug, meta: "Go to" },
  { to: "/personalisation", label: "Personalisation", group: "Workspace", icon: IconSettings, meta: "Go to" },
  { to: "/security", label: "Security", group: "Management", icon: IconShieldLock, meta: "Go to" },
  { to: "/tokens", label: "API tokens", group: "Management", icon: IconKey, meta: "Go to" },
  { to: "/admin/members", label: "Members", group: "Management", icon: IconUserCheck, meta: "Go to" },
  { to: "/admin/teams", label: "Teams", group: "Management", icon: IconUsers, meta: "Go to" },
  { to: "/admin/roles", label: "Roles", group: "Management", icon: IconLock, meta: "Go to" },
];

function highlightMatch(text: string, query: string): ReactNode {
  if (!query.trim()) return text;
  const index = text.toLowerCase().indexOf(query.toLowerCase().trim());
  if (index === -1) return text;
  const before = text.slice(0, index);
  const match = text.slice(index, index + query.trim().length);
  const after = text.slice(index + query.trim().length);
  return (
    <>
      {before}
      <mark>{match}</mark>
      {after}
    </>
  );
}

export function CommandLauncher() {
  const [opened, { open, close, toggle }] = useDisclosure(false);
  const [query, setQuery] = useState("");
  const [selectedIndex, setSelectedIndex] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const navigate = useNavigate();

  const filtered = useMemo(() => {
    if (!query.trim()) return items;
    const q = query.toLowerCase().trim();
    return items.filter(
      (item) => item.label.toLowerCase().includes(q) || item.group.toLowerCase().includes(q),
    );
  }, [query]);

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

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setSelectedIndex((prev) => (filtered.length > 0 ? (prev + 1) % filtered.length : 0));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setSelectedIndex((prev) => (filtered.length > 0 ? (prev - 1 + filtered.length) % filtered.length : 0));
    } else if (e.key === "Enter") {
      e.preventDefault();
      const target = filtered[selectedIndex];
      if (target) {
        close();
        void navigate({ to: target.to });
      }
    } else if (e.key === "Escape") {
      close();
    }
  };

  return (
    <>
      <UnstyledButton
        className="shell-launcher"
        aria-label="Search or jump to"
        onClick={() => {
          setQuery("");
          setSelectedIndex(0);
          open();
        }}
      >
        <IconSearch size={16} stroke={1.75} aria-hidden style={{ color: "var(--ink-muted)" }} />
        <span className="shell-launcher-text">Search or jump to…</span>
        <KbdGroup className="shell-launcher-keys">
          <Kbd>⌘</Kbd>
          <Kbd>K</Kbd>
        </KbdGroup>
      </UnstyledButton>
      <Dialog
        opened={opened}
        onClose={close}
        title="Go to"
        aria-label="Go to"
        size={560}
        radius="xl"
        className="u-cmd-modal"
        withCloseButton={false}
        padding={0}
      >
        <div className="u-cmd">
          <div className="u-cmd-input">
            <IconSearch size={18} stroke={1.75} aria-hidden style={{ color: "var(--ink-muted)" }} />
            <input
              ref={inputRef}
              placeholder="Search or jump to…"
              value={query}
              onChange={(e) => {
                setQuery(e.target.value);
                setSelectedIndex(0);
              }}
              onKeyDown={handleKeyDown}
              autoFocus
              aria-label="Search or jump to"
            />
            {/* Local key cap: the design-system Kbd is not clickable. */}
            <kbd className="u-kbd" onClick={close} style={{ cursor: "pointer" }}>
              Esc
            </kbd>
          </div>
          <div className="u-cmd-list">
            {filtered.length === 0 ? (
              <div className="u-cmd-item" style={{ color: "var(--ink-muted)", cursor: "default" }}>
                No match found for &ldquo;{query}&rdquo;.
              </div>
            ) : (
              filtered.map((item, index) => {
                const isSelected = index === selectedIndex;
                const Icon = item.icon;
                return (
                  <Link
                    key={`${item.to}:${item.label}`}
                    to={item.to}
                    className="u-cmd-item"
                    data-active={isSelected ? "true" : undefined}
                    aria-selected={isSelected}
                    onClick={close}
                    onMouseMove={() => setSelectedIndex(index)}
                  >
                    <Icon size={18} stroke={1.75} className="u-icon" aria-hidden="true" />
                    <span>{highlightMatch(item.label, query)}</span>
                    <span className="u-cmd-meta" aria-hidden="true">
                      {item.meta}
                    </span>
                  </Link>
                );
              })
            )}
          </div>
          <div className="u-cmd-foot">
            <span>
              <KbdGroup>
                <Kbd>↑</Kbd>
                <Kbd>↓</Kbd>
              </KbdGroup>{" "}
              move
            </span>
            <span>
              <Kbd>Enter</Kbd> open
            </span>
            <span>
              <KbdGroup>
                <Kbd>⌘</Kbd>
                <Kbd>K</Kbd>
              </KbdGroup>{" "}
              toggle
            </span>
          </div>
        </div>
      </Dialog>
    </>
  );
}
