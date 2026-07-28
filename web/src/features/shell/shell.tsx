import {
  ActionIcon,
  Anchor,
  AppShell,
  Badge,
  Breadcrumbs,
  Button,
  Group,
  NavLink,
  Popover,
  Text,
  Tooltip,
  useComputedColorScheme,
} from "@mantine/core";
import { useMediaQuery } from "@mantine/hooks";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, Outlet, useRouterState } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";

import { ThemeSwitch } from "../../theme";
import {
  type AppNotification,
  type NotificationKind,
  useApi,
} from "./api";
import { ShellIcon, type ShellConcept } from "./icons";
import wordmarkDark from "./wordmark-dark.svg";
import wordmarkLight from "./wordmark-light.svg";
import { WorkspaceName } from "./workspace";

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

const kindOrder = {
  approval: 0,
  question: 1,
  finished: 2,
  failed: 3,
} as const satisfies Record<NotificationKind, number>;

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
  return pathname === to;
}

function ordered(items: AppNotification[]) {
  return [...items].sort((left, right) => kindOrder[left.kind] - kindOrder[right.kind]);
}

function needsApproval(kind: NotificationKind) {
  return kind === "approval" || kind === "question";
}

function Wordmark() {
  const scheme = useComputedColorScheme("light", { getInitialValueInEffect: false });
  return (
    <img
      className="shell-wordmark"
      src={scheme === "dark" ? wordmarkDark : wordmarkLight}
      alt="Upmo"
    />
  );
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
        activeOptions={{ exact: item.to !== "/settings" }}
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
  if (pathname === "/settings/connectors") {
    return (
      <Breadcrumbs separator="/">
        <Anchor component={Link} to="/settings" size="sm">
          Settings
        </Anchor>
        <Text size="sm" fw={600} aria-current="page">
          Connectors
        </Text>
      </Breadcrumbs>
    );
  }

  const label =
    primary.find((item) => item.to === pathname)?.label ??
    (pathname === "/settings" ? "Settings" : "");

  return (
    <Breadcrumbs separator="/">
      <Text size="sm" fw={600} aria-current="page">
        {label}
      </Text>
    </Breadcrumbs>
  );
}

function NotificationsBell() {
  const api = useApi();
  const queryClient = useQueryClient();
  const notifications = useQuery({
    queryKey: ["notifications"],
    queryFn: () => api.listNotifications(),
  });
  const approve = useMutation({
    mutationFn: (id: string) => api.approveNotification(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["notifications"] }),
  });
  const items = ordered(notifications.data ?? []);
  const count = items.length;
  const label = count > 0 ? `Notifications, ${count} unread` : "Notifications";

  return (
    <Popover position="bottom-end" width={320} shadow="md">
      <Popover.Target>
        <ActionIcon className="shell-bell" aria-label={label} variant="subtle" size={40}>
          <ShellIcon concept="nav.notifications" />
          {count > 0 ? (
            <Badge className="shell-count" size="xs" circle aria-hidden>
              {count}
            </Badge>
          ) : null}
        </ActionIcon>
      </Popover.Target>
      <Popover.Dropdown>
        {notifications.data && items.length === 0 ? (
          <>
            <Text fw={600}>You are up to date</Text>
            <Text c="dimmed" size="sm">
              Approvals and finished tasks show up here.
            </Text>
          </>
        ) : (
          <ul className="shell-notes" aria-label="Notifications">
            {items.map((item) => (
              <li key={item.id} className="shell-note">
                <span>{item.title}</span>
                {needsApproval(item.kind) ? (
                  <Button
                    size="xs"
                    variant="light"
                    disabled={approve.isPending}
                    onClick={() => approve.mutate(item.id)}
                  >
                    Approve
                  </Button>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </Popover.Dropdown>
    </Popover>
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
          <ThemeSwitch />
          <span className="code-sm">0.1.0</span>
        </div>
      </AppShell.Navbar>
      <AppShell.Main>
        <div className="shell-content">
          <Outlet />
        </div>
      </AppShell.Main>
    </AppShell>
  );
}
