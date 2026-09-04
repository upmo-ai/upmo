import {
  ActionIcon,
  Alert,
  Anchor,
  AppShell,
  Breadcrumbs,
  Button,
  CloseButton,
  Group,
  NavLink,
  Text,
  Tooltip,
} from "@mantine/core";
import { useMediaQuery } from "@mantine/hooks";
import { useMutationState, useQueryClient } from "@tanstack/react-query";
import { Link, Outlet, useNavigate, useRouterState } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";

import { PasskeyOffer } from "../features/identity/passkey-offer";
import { NotificationsBell } from "../features/notifications/bell";
import { adminSections, settingsSections } from "../features/settings/settings";
import { WorkspaceName } from "../features/workspaces/workspace";
import { ThemeSwitch } from "../theme";
import { ShellIcon, type ShellConcept } from "../ui/icons";
import { BrandLink } from "./brand";
import { CommandLauncher } from "./command-launcher";
import { endThisSession } from "./passkeys";

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
const adminItem = { to: "/admin", label: "Admin Settings", concept: "nav.settings" } as const;

type NavItem = {
  to: (typeof primary)[number]["to"] | (typeof settingsItem)["to"] | (typeof adminItem)["to"];
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
  if (to === "/settings" || to === "/admin" || to === "/tasks") {
    return pathname === to || pathname.startsWith(`${to}/`);
  }
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
        activeOptions={{ exact: item.to !== "/settings" && item.to !== "/admin" && item.to !== "/tasks" }}
        active={current}
        aria-current={current ? "page" : undefined}
        aria-label={item.label}
        label={<span className="shell-nav-label">{item.label}</span>}
        leftSection={<ShellIcon concept={item.concept} size={16} />}
        variant={current ? "light" : "subtle"}
        onClick={onNavigate}
      />
    </Tooltip>
  );
}

function ShellBreadcrumb({ pathname }: { pathname: string }) {
  const settingsSection = settingsSections.find((item) => item.to === pathname);
  if (settingsSection) {
    return (
      <Breadcrumbs separator="/">
        <Anchor component={Link} to="/settings" size="sm">
          Settings
        </Anchor>
        <Text size="sm" fw={600} aria-current="page">
          {settingsSection.label}
        </Text>
      </Breadcrumbs>
    );
  }

  const adminSection = adminSections.find((item) => item.to === pathname);
  if (adminSection) {
    return (
      <Breadcrumbs separator="/">
        <Anchor component={Link} to="/admin" size="sm">
          Admin Settings
        </Anchor>
        <Text size="sm" fw={600} aria-current="page">
          {adminSection.label}
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
        : pathname === "/admin"
          ? "Admin Settings"
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
  const queryClient = useQueryClient();
  return (
    <Button
      className="shell-sign-out"
      variant="subtle"
      onClick={async () => {
        await endThisSession();
        queryClient.clear();
        await navigate({ to: "/login" });
      }}
    >
      Log out
    </Button>
  );
}

export function Shell() {
  const layout = useShellLayout();
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  const [opened, setOpened] = useState(false);
  const [dismissedAt, setDismissedAt] = useState<number>(0);
  const previous = useRef(pathname);

  const failedMutations = useMutationState({
    filters: { status: "error" },
    select: (mutation) => ({
      error: mutation.state.error,
      meta: mutation.options.meta as { shown?: boolean } | undefined,
      submittedAt: mutation.state.submittedAt,
    }),
  });

  const unshownErrors = failedMutations.filter(
    (m) => !m.meta?.shown && m.error && m.submittedAt > dismissedAt,
  );
  const latestMutation = unshownErrors[unshownErrors.length - 1];
  const errorMessage = latestMutation?.error
    ? latestMutation.error instanceof Error
      ? latestMutation.error.message
      : String(latestMutation.error)
    : null;

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
          <Group gap="xs" wrap="nowrap">
            <CommandLauncher />
            <NotificationsBell />
          </Group>
        </Group>
      </AppShell.Header>
      <AppShell.Navbar className="shell-navbar" aria-label="Sidebar" data-layout={layout}>
        <BrandLink className="shell-wordmark" />
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
          <NavEntry
            item={adminItem}
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
          {errorMessage ? (
            <Alert
              className="shell-message"
              role="alert"
              color="red"
              variant="light"
            >
              <Group justify="space-between" align="center" wrap="nowrap">
                <Text>{errorMessage}</Text>
                <CloseButton
                  aria-label="Close message"
                  onClick={() => setDismissedAt(latestMutation.submittedAt)}
                />
              </Group>
            </Alert>
          ) : null}
          <PasskeyOffer />
          <Outlet />
        </div>
      </AppShell.Main>
    </AppShell>
  );
}
