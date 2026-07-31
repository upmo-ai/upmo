import {
  ActionIcon,
  Anchor,
  AppShell,
  Breadcrumbs,
  Button,
  Group,
  NavLink,
  Text,
  Tooltip,
} from "@mantine/core";
import { useMediaQuery } from "@mantine/hooks";
import { Link, Outlet, useNavigate, useRouterState } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";

import { PasskeyOffer } from "../features/identity/passkey-offer";
import { NotificationsBell } from "../features/notifications/bell";
import { settingsSections } from "../features/settings/settings";
import { WorkspaceName } from "../features/workspaces/workspace";
import { ThemeSwitch } from "../theme";
import { ShellIcon, type ShellConcept } from "../ui/icons";
import { Wordmark } from "../ui/wordmark";

import "./shell.css";

const primary = [
  { to: "/", label: "Home", concept: "nav.home" },
  { to: "/chat", label: "Chat", concept: "nav.chats" },
  { to: "/tasks", label: "Tasks", concept: "nav.tasks" },
  { to: "/automations", label: "Automations", concept: "nav.automations" },
  { to: "/agents", label: "Agents", concept: "nav.agents" },
  { to: "/knowledge", label: "Knowledge", concept: "nav.knowledge" },
  { to: "/usage", label: "Usage", concept: "nav.usage" },
] as const;

const settingsItem = { to: "/settings", label: "Settings", concept: "nav.settings" } as const;

type NavItem = {
  to: (typeof primary)[number]["to"] | (typeof settingsItem)["to"];
  label: string;
  concept: ShellConcept;
};

function useShellLayout(): "drawer" | "rail" | "full" {
  const full = useMediaQuery("(min-width: 62em)", false, { getInitialValueInEffect: false });
  const atLeastSm = useMediaQuery("(min-width: 48em)", false, { getInitialValueInEffect: false });
  if (full) return "full";
  if (atLeastSm) return "rail";
  return "drawer";
}

function isCurrent(pathname: string, to: string) {
  if (to === "/") return pathname === "/";
  if (to === "/settings") return pathname === "/settings" || pathname.startsWith("/settings/");
  if (to === "/tasks") return pathname === "/tasks" || pathname.startsWith("/tasks/");
  return pathname === to;
}

function NavEntry({
  item,
  pathname,
  rail,
  onNavigate,
}: {
  item: NavItem;
  pathname: string;
  rail: boolean;
  onNavigate: () => void;
}) {
  const current = isCurrent(pathname, item.to);
  return (
    <Tooltip label={item.label} disabled={!rail} position="right" openDelay={400}>
      <NavLink
        className="shell-nav-link"
        component={Link}
        to={item.to}
        activeOptions={{ exact: item.to !== "/settings" && item.to !== "/tasks" }}
        active={current}
        aria-current={current ? "page" : undefined}
        aria-label={item.label}
        label={<span className="shell-nav-label">{item.label}</span>}
        leftSection={<ShellIcon concept={item.concept} />}
        variant={current ? "light" : "subtle"}
        onClick={onNavigate}
      />
    </Tooltip>
  );
}

function ShellBreadcrumb({ pathname }: { pathname: string }) {
  const section = settingsSections.find((item) => item.to === pathname);
  if (section) {
    return (
      <Breadcrumbs separator="/">
        <Anchor component={Link} to="/settings" size="sm">
          Settings
        </Anchor>
        <Text size="sm" fw={600} aria-current="page">
          {section.label}
        </Text>
      </Breadcrumbs>
    );
  }

  const label =
    primary.find((item) => item.to === pathname)?.label ??
    (pathname.startsWith("/tasks/")
      ? "Tasks"
      : pathname === "/settings"
        ? "Settings"
        : "");

  return (
    <Breadcrumbs separator="/">
      <Text size="sm" fw={600} aria-current="page">
        {label}
      </Text>
    </Breadcrumbs>
  );
}

function SignOut() {
  const navigate = useNavigate();
  return (
    <Button
      className="shell-sign-out"
      variant="subtle"
      onClick={() => navigate({ to: "/sign-in" })}
    >
      Sign out
    </Button>
  );
}

export function Shell() {
  const layout = useShellLayout();
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  const [opened, setOpened] = useState(false);
  const previous = useRef(pathname);

  useEffect(() => {
    if (previous.current === pathname) return;
    previous.current = pathname;
    setOpened(false);
  }, [pathname]);

  const close = () => setOpened(false);

  return (
    <AppShell
      padding={0}
      header={{ height: 48 }}
      navbar={{
        width: { sm: 56, md: 248 },
        breakpoint: "sm",
        collapsed: { mobile: !opened },
      }}
    >
      <AppShell.Header className="shell-header">
        <Group h="100%" px="sm" gap="xs" wrap="nowrap" justify="space-between">
          <Group gap="xs" wrap="nowrap">
            <Tooltip label="Toggle sidebar" position="bottom" openDelay={400}>
              <ActionIcon
                aria-label="Toggle sidebar"
                variant="subtle"
                size={40}
                onClick={() => setOpened((value) => !value)}
              >
                <ShellIcon concept="nav.sidebar" />
              </ActionIcon>
            </Tooltip>
            <ShellBreadcrumb pathname={pathname} />
          </Group>
          <NotificationsBell />
        </Group>
      </AppShell.Header>
      <AppShell.Navbar className="shell-navbar" aria-label="Sidebar" data-layout={layout}>
        <Wordmark />
        <WorkspaceName />
        <div className="shell-nav-scroll">
          {primary.map((item) => (
            <NavEntry
              key={item.to}
              item={item}
              pathname={pathname}
              rail={layout === "rail"}
              onNavigate={close}
            />
          ))}
          <Text className="shell-group">Workspace</Text>
          <NavEntry
            item={settingsItem}
            pathname={pathname}
            rail={layout === "rail"}
            onNavigate={close}
          />
        </div>
        <div className="shell-footer">
          <div className="shell-footer-meta">
            <ThemeSwitch />
            <span className="code-sm">0.1.0</span>
          </div>
          <SignOut />
        </div>
      </AppShell.Navbar>
      <AppShell.Main>
        <div className="shell-content">
          <PasskeyOffer />
          <Outlet />
        </div>
      </AppShell.Main>
    </AppShell>
  );
}
