import {
  Alert,
  AppShell,
  CloseButton,
  Group,
  NavLink,
  Text,
  UnstyledButton,
} from "@mantine/core";
import { useMediaQuery } from "@mantine/hooks";
import { useMutationState, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, Outlet, useNavigate, useRouterState } from "@tanstack/react-router";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  IconKey,
  IconLogout,
  IconMoon,
  IconPlug,
  IconSelector,
  IconServer,
  IconShieldCheck,
  IconSparkles,
  IconSun,
} from "@tabler/icons-react";
import {
  Breadcrumbs,
  IconButton,
  Menu,
  Tooltip,
  useUpmoTheme,
  type BreadcrumbItem,
} from "@upmo-ai/components";

import { sessionQueryKey, useApi } from "./api";
import { PasskeyOffer } from "../features/identity/passkey-offer";
import { NotificationsBell } from "../features/notifications/bell";
import { adminSections, settingsSections } from "../features/settings/settings";
import { WorkspaceName, workspaceName } from "../features/workspaces/workspace";
import { ThemeSwitch, useThemeChoice } from "../theme";
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

type NavItem = {
  to: string;
  label: string;
  concept?: ShellConcept;
  icon?: React.ReactNode;
};

const workspaceItems: NavItem[] = [
  {
    to: "/providers",
    label: "Providers",
    icon: <IconServer size={16} stroke={1.75} aria-hidden="true" />,
  },
  {
    to: "/connectors",
    label: "Connectors",
    icon: <IconPlug size={16} stroke={1.75} aria-hidden="true" />,
  },
];

const managementSubItems = [
  { to: "/admin/members", label: "Members" },
  { to: "/admin/teams", label: "Teams" },
  { to: "/admin/roles", label: "Roles" },
] as const;

function useShellLayout(): "drawer" | "rail" | "full" {
  const full = useMediaQuery("(min-width: 62em)", false, { getInitialValueInEffect: false });
  const atLeastSm = useMediaQuery("(min-width: 48em)", false, { getInitialValueInEffect: false });
  if (full) return "full";
  if (atLeastSm) return "rail";
  return "drawer";
}

function isCurrent(pathname: string, to: string) {
  if (to === "/") return pathname === "/";
  if (to === "/connectors") {
    return pathname === "/connectors" || pathname.startsWith("/connectors/");
  }
  if (to === "/providers") {
    return pathname === "/providers" || pathname.startsWith("/providers/");
  }
  if (to === "/admin" || to === "/tasks") {
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
        activeOptions={{
          exact:
            item.to !== "/admin" &&
            item.to !== "/tasks" &&
            item.to !== "/connectors" &&
            item.to !== "/providers",
        }}
        active={current}
        aria-current={current ? "page" : undefined}
        aria-label={item.label}
        label={<span className="shell-nav-label">{item.label}</span>}
        leftSection={item.icon ?? (item.concept ? <ShellIcon concept={item.concept} size={16} /> : null)}
        variant="subtle"
        onClick={onNavigate}
      />
    </Tooltip>
  );
}

function crumb(label: string, to: string): BreadcrumbItem {
  return { label, component: Link, linkProps: { to } };
}

function shellCrumbs(pathname: string): BreadcrumbItem[] {
  if (pathname === "/connectors") {
    return [crumb("Workspace", "/connectors"), { label: "Connectors" }];
  }

  if (pathname === "/connectors/new") {
    return [crumb("Workspace", "/connectors"), crumb("Connectors", "/connectors"), { label: "Connect tool" }];
  }

  if (pathname.startsWith("/connectors/") && pathname.endsWith("/edit")) {
    return [
      crumb("Workspace", "/connectors"),
      crumb("Connectors", "/connectors"),
      { label: "Edit permissions" },
    ];
  }

  if (pathname === "/providers") {
    return [crumb("Workspace", "/providers"), { label: "Providers" }];
  }

  if (pathname === "/providers/new") {
    return [crumb("Workspace", "/providers"), crumb("Providers", "/providers"), { label: "Connect provider" }];
  }

  if (pathname.startsWith("/providers/") && pathname.endsWith("/edit")) {
    return [crumb("Workspace", "/providers"), crumb("Providers", "/providers"), { label: "Edit provider" }];
  }

  const adminSection = adminSections.find(
    (item) => item.to === pathname || (item.to === "/admin/members" && pathname === "/admin"),
  );
  if (adminSection) {
    return [crumb("Management", "/admin/members"), { label: adminSection.label }];
  }

  const personalSection = settingsSections.find((item) => item.to === pathname);
  if (personalSection) {
    return [{ label: personalSection.label }];
  }

  const label =
    primary.find((item) => item.to === pathname)?.label ??
    (pathname.startsWith("/tasks/")
      ? "Tasks"
      : pathname === "/admin"
        ? "Management"
        : "");

  return [{ label }];
}

function ShellBreadcrumb({ pathname }: { pathname: string }) {
  return <Breadcrumbs items={shellCrumbs(pathname)} />;
}

export function Shell() {
  const api = useApi();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const layout = useShellLayout();
  const { setChoice } = useThemeChoice();
  const resolvedTheme = useUpmoTheme();
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  const [opened, setOpened] = useState(false);
  const [desktopOpened, setDesktopOpened] = useState(true);
  const [dismissedAt, setDismissedAt] = useState<number>(0);
  const previous = useRef(pathname);

  const toggleSidebar = useCallback(() => {
    setDesktopOpened((val) => !val);
    setOpened((val) => !val);
  }, []);

  const session = useQuery({
    queryKey: sessionQueryKey(),
    queryFn: () => api.session.get(),
  });
  const members = useQuery({
    queryKey: ["members"],
    queryFn: () => (api.members?.list ? api.members.list() : Promise.resolve([])),
  });
  const currentMember = members.data?.find((m) => m.id === session.data?.memberId);
  const userName = currentMember?.name ?? "Muhammad Sumon Molla Selim";
  const userRole = session.data?.view ?? currentMember?.role ?? "owner";
  const roleName = userRole.charAt(0).toUpperCase() + userRole.slice(1);
  const isManagement = userRole === "owner" || userRole === "admin";
  const initials =
    userName
      .split(" ")
      .map((w) => w[0])
      .filter(Boolean)
      .join("")
      .slice(0, 2)
      .toUpperCase() || "U";

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

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "b") {
        event.preventDefault();
        toggleSidebar();
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [toggleSidebar]);

  const close = () => setOpened(false);
  const effectiveLayout = !desktopOpened ? "rail" : layout;

  return (
    <AppShell
      className="shell"
      layout="alt"
      padding={0}
      header={{ height: 48 }}
      navbar={{
        width: { sm: 56, md: desktopOpened ? 248 : 56 },
        breakpoint: "sm",
        collapsed: {
          mobile: !opened,
        },
      }}
    >
      <a className="shell-skip" href="#content">
        Skip to content
      </a>
      <AppShell.Header className="shell-header">
        <div className="shell-header-inner">
          <div className="shell-header-left">
            <IconButton
              className="shell-header-toggle"
              aria-label="Toggle sidebar"
              variant="subtle"
              size={36}
              onClick={toggleSidebar}
            >
              <ShellIcon concept="nav.sidebar" />
            </IconButton>
            <ShellBreadcrumb pathname={pathname} />
          </div>
          <div className="shell-header-center">
            <CommandLauncher />
          </div>
          <div className="shell-header-right">
            <NotificationsBell />
          </div>
        </div>
      </AppShell.Header>
      <AppShell.Navbar className="shell-navbar" aria-label="Sidebar" data-layout={effectiveLayout}>
        <div className="shell-nav-head">
          <div className="shell-brand-group">
            <BrandLink className="shell-brand-link" />
            <WorkspaceName />
          </div>
          <IconButton
            className="shell-nav-toggle"
            aria-label="Toggle sidebar"
            variant="subtle"
            size={32}
            onClick={toggleSidebar}
          >
            <ShellIcon concept="nav.sidebar" size={16} />
          </IconButton>
        </div>
        <div className="shell-nav-scroll">
          {primary.map((item) => (
            <NavEntry
              key={item.to}
              item={item}
              pathname={pathname}
              rail={effectiveLayout === "rail"}
              onNavigate={close}
            />
          ))}
          <Text className="shell-group">Workspace</Text>
          {workspaceItems.map((item) => (
            <NavEntry
              key={item.to}
              item={item}
              pathname={pathname}
              rail={effectiveLayout === "rail"}
              onNavigate={close}
            />
          ))}
          {isManagement && (
            effectiveLayout === "rail" ? (
              <Menu
                position="right-start"
                offset={8}
                width={180}
                withinPortal
                transitionProps={{ duration: 0 }}
              >
                <Menu.Target>
                  <IconButton
                    className="shell-nav-link"
                    variant="subtle"
                    size={32}
                    aria-label="Management"
                    data-active={pathname.startsWith("/admin") ? "true" : undefined}
                    tooltipPosition="right"
                  >
                    <ShellIcon concept="nav.settings" size={16} />
                  </IconButton>
                </Menu.Target>
                <Menu.Dropdown>
                  <Menu.Label>Management</Menu.Label>
                  {managementSubItems.map((sub) => (
                    <Menu.Item
                      key={sub.to}
                      component={Link}
                      to={sub.to}
                      onClick={close}
                    >
                      {sub.label}
                    </Menu.Item>
                  ))}
                </Menu.Dropdown>
              </Menu>
            ) : (
              <NavLink
                className="shell-nav-link shell-nav-parent"
                label={<span className="shell-nav-label">Management</span>}
                leftSection={<ShellIcon concept="nav.settings" size={16} />}
                defaultOpened
                childrenOffset={0}
                active={false}
              >
                {managementSubItems.map((sub) => {
                  const isSubActive =
                    pathname === sub.to || (sub.to === "/admin/members" && pathname === "/admin");
                  return (
                    <NavLink
                      key={sub.to}
                      className="shell-nav-link shell-nav-sublink"
                      component={Link}
                      to={sub.to}
                      activeOptions={{ exact: true }}
                      active={isSubActive}
                      aria-current={isSubActive ? "page" : undefined}
                      aria-label={sub.label}
                      label={<span className="shell-nav-label">{sub.label}</span>}
                      variant="subtle"
                      onClick={close}
                    />
                  );
                })}
              </NavLink>
            )
          )}
        </div>
        <div className="shell-footer">
          <Menu
            position={effectiveLayout === "rail" ? "right-end" : "top-start"}
            offset={8}
            shadow="md"
            width={240}
            withinPortal
            transitionProps={{ duration: 0 }}
          >
            <Menu.Target>
              <UnstyledButton
                className="shell-user-card"
                aria-label={`User account: ${userName}`}
              >
                <span className="shell-avatar" aria-hidden="true">
                  {initials}
                </span>
                <div className="shell-user-info">
                  <span className="shell-user-name" title={userName}>
                    {userName}
                  </span>
                  <span className="shell-user-desc" title={`${roleName} · ${workspaceName}`}>
                    {roleName} · {workspaceName}
                  </span>
                </div>
                <IconSelector size={14} stroke={1.75} className="shell-user-chevron" aria-hidden="true" />
              </UnstyledButton>
            </Menu.Target>

            <Menu.Dropdown className="shell-user-dropdown">
              <div className="shell-user-dropdown-header">
                <span className="shell-avatar" aria-hidden="true">
                  {initials}
                </span>
                <div className="shell-user-info">
                  <span className="shell-user-name" title={userName}>
                    {userName}
                  </span>
                  <span className="shell-user-desc" title={`${roleName} · ${workspaceName}`}>
                    {roleName} · {workspaceName}
                  </span>
                </div>
              </div>
              <Menu.Divider />
              <Menu.Item
                component={Link}
                to="/personalisation"
                leftSection={<IconSparkles size={16} stroke={1.75} aria-hidden="true" />}
                onClick={close}
              >
                Personalisation
              </Menu.Item>
              <Menu.Item
                component={Link}
                to="/security"
                leftSection={<IconShieldCheck size={16} stroke={1.75} aria-hidden="true" />}
                onClick={close}
              >
                Security
              </Menu.Item>
              <Menu.Item
                component={Link}
                to="/tokens"
                leftSection={<IconKey size={16} stroke={1.75} aria-hidden="true" />}
                onClick={close}
              >
                API tokens
              </Menu.Item>
              <Menu.Divider />
              <Menu.Item
                color="red"
                className="shell-menu-logout"
                leftSection={<IconLogout size={16} stroke={1.75} aria-hidden="true" />}
                onClick={async () => {
                  close();
                  await endThisSession();
                  queryClient.clear();
                  await navigate({ to: "/login" });
                }}
              >
                Log out
              </Menu.Item>
            </Menu.Dropdown>
          </Menu>

          <div className="shell-theme-row">
            <ThemeSwitch />
          </div>
          <IconButton
            className="shell-rail-theme-toggle"
            aria-label="Toggle colour scheme"
            variant="subtle"
            size={32}
            onClick={() => setChoice(resolvedTheme === "dark" ? "light" : "dark")}
            tooltipPosition="right"
          >
            {resolvedTheme === "dark" ? (
              <IconSun size={16} stroke={1.75} aria-hidden="true" />
            ) : (
              <IconMoon size={16} stroke={1.75} aria-hidden="true" />
            )}
          </IconButton>
          <div className="shell-footer-bar">
            <span className="shell-version">v0.1.0</span>
          </div>
        </div>
      </AppShell.Navbar>
      <AppShell.Main id="content">
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
