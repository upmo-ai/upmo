import { Text, UnstyledButton, useMantineTheme } from "@mantine/core";
import { useMediaQuery } from "@mantine/hooks";
import { useMutationState, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, Outlet, useNavigate, useRouterState } from "@tanstack/react-router";
import { useCallback, useEffect, useState } from "react";
import {
  IconKey,
  IconLogout,
  IconMoon,
  IconPlug,
  IconRoute,
  IconSelector,
  IconServer,
  IconShieldCheck,
  IconSparkles,
  IconSun,
} from "@tabler/icons-react";
import {
  AppFrame,
  Breadcrumbs,
  IconButton,
  Menu,
  NavItem,
  NavSection,
  SideNav,
  StatusAlert,
  Wordmark,
  useAppFrame,
  useUpmoTheme,
  type BreadcrumbItem,
} from "@upmo-ai/components";

import { sessionQueryKey, useApi } from "./api";
import { PasskeyOffer } from "../features/identity/passkey-offer";
import { NotificationsBell } from "../features/notifications/bell";
import { TasksLiveness } from "../features/tasks/liveness";
import { adminSections, settingsSections } from "../features/settings/settings";
import { WorkspaceName, workspaceName } from "../features/workspaces/workspace";
import { ThemeSwitch, useThemeChoice } from "../theme";
import { ShellIcon } from "../ui/icons";
import { CommandLauncher } from "./command-launcher";
import { fillsMain } from "./layout";
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

const workspaceItems = [
  {
    to: "/settings/providers",
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

function isCurrent(pathname: string, to: string) {
  if (to === "/") return pathname === "/";
  if (to === "/connectors") {
    return pathname === "/connectors" || pathname.startsWith("/connectors/");
  }
  if (to === "/settings/providers") {
    return pathname === "/settings/providers" || pathname.startsWith("/settings/providers/");
  }
  if (to === "/admin" || to === "/tasks" || to === "/knowledge") {
    return pathname === to || pathname.startsWith(`${to}/`);
  }
  return pathname === to;
}

function underAdmin(pathname: string) {
  return isCurrent(pathname, "/admin");
}

function isManagementCurrent(pathname: string, to: string) {
  return isCurrent(pathname, to) || (to === "/admin/members" && pathname === "/admin");
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

  if (pathname === "/settings/models") {
    return [crumb("Workspace", "/settings/models"), { label: "Models and routes" }];
  }

  if (pathname === "/settings/providers") {
    return [crumb("Workspace", "/settings/providers"), { label: "Providers" }];
  }

  if (pathname === "/settings/providers/new") {
    return [
      crumb("Workspace", "/settings/providers"),
      crumb("Providers", "/settings/providers"),
      { label: "Connect provider" },
    ];
  }

  if (pathname.startsWith("/settings/providers/") && pathname.endsWith("/edit")) {
    return [
      crumb("Workspace", "/settings/providers"),
      crumb("Providers", "/settings/providers"),
      { label: "Edit provider" },
    ];
  }

  if (pathname.startsWith("/settings/providers/") && pathname.endsWith("/prices")) {
    return [crumb("Workspace", "/settings/providers"), crumb("Providers", "/settings/providers"), { label: "Prices" }];
  }

  if (pathname.startsWith("/knowledge/")) {
    return [crumb("Knowledge", "/knowledge"), { label: "Folder" }];
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

type ShellUser = {
  name: string;
  initials: string;
  roleName: string;
  management: boolean;
};

function ShellHeader() {
  const { rail, closeDrawer } = useAppFrame();
  return (
    <>
      <Link to="/" className="wordmark-link" aria-label="Upmo" onClick={closeDrawer}>
        <Wordmark decorative compact={rail} height={rail ? 22 : 20} />
      </Link>
      {rail ? null : <WorkspaceName />}
    </>
  );
}

function ShellNavigation({
  pathname,
  management,
}: {
  pathname: string;
  management: boolean;
}) {
  const { rail } = useAppFrame();
  const inAdmin = underAdmin(pathname);
  const [managementOpened, setManagementOpened] = useState(inAdmin);
  const [wasInAdmin, setWasInAdmin] = useState(inAdmin);

  if (inAdmin !== wasInAdmin) {
    setWasInAdmin(inAdmin);
    if (inAdmin) setManagementOpened(true);
  }

  return (
    <SideNav label="Main">
      <NavSection>
        {primary.map((item) => (
          <NavItem
            key={item.to}
            label={item.label}
            icon={<ShellIcon concept={item.concept} size={16} />}
            component={Link}
            linkProps={{ to: item.to }}
            active={isCurrent(pathname, item.to)}
          />
        ))}
      </NavSection>
      <NavSection label="Workspace">
        {workspaceItems.map((item) => (
          <NavItem
            key={item.to}
            label={item.label}
            icon={item.icon}
            component={Link}
            linkProps={{ to: item.to }}
            active={isCurrent(pathname, item.to)}
          />
        ))}
        <NavItem
          label="Models and routes"
          icon={<IconRoute size={16} stroke={1.75} aria-hidden="true" />}
          component={Link}
          linkProps={{ to: "/settings/models" }}
          active={isCurrent(pathname, "/settings/models")}
        />
      </NavSection>
      {management ? (
        <NavSection>
          <NavItem
            label="Management"
            icon={<ShellIcon concept="nav.settings" size={16} />}
            active={inAdmin && (rail || !managementOpened)}
            opened={managementOpened}
            onOpenedChange={setManagementOpened}
          >
            {managementSubItems.map((item) => (
              <NavItem
                key={item.to}
                label={item.label}
                component={Link}
                linkProps={{ to: item.to }}
                active={isManagementCurrent(pathname, item.to)}
              />
            ))}
          </NavItem>
        </NavSection>
      ) : null}
    </SideNav>
  );
}

function ShellTopBar({ pathname }: { pathname: string }) {
  return (
    <div className="shell-top-bar">
      <div className="shell-top-bar-start">
        <Breadcrumbs items={shellCrumbs(pathname)} />
      </div>
      <CommandLauncher />
      <div className="shell-top-bar-end">
        <TasksLiveness />
        <NotificationsBell />
      </div>
    </div>
  );
}

function ShellFooter({ user }: { user: ShellUser }) {
  const { rail, closeDrawer } = useAppFrame();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const { setChoice } = useThemeChoice();
  const resolvedTheme = useUpmoTheme();
  const summary = `${user.roleName} · ${workspaceName}`;

  const account = (
    <Menu
      position={rail ? "right-end" : "top-start"}
      offset={8}
      shadow="md"
      width={240}
      withinPortal
      transitionProps={{ duration: 0 }}
    >
      <Menu.Target>
        <UnstyledButton
          className="shell-user-card"
          data-rail={rail || undefined}
          aria-label={`User account: ${user.name}`}
        >
          <span className="shell-avatar" aria-hidden="true">
            {user.initials}
          </span>
          {rail ? null : (
            <>
              <div className="shell-user-info">
                <span className="shell-user-name" title={user.name}>
                  {user.name}
                </span>
                <span className="shell-user-desc" title={summary}>
                  {summary}
                </span>
              </div>
              <IconSelector size={14} stroke={1.75} className="shell-user-chevron" aria-hidden="true" />
            </>
          )}
        </UnstyledButton>
      </Menu.Target>

      <Menu.Dropdown className="shell-user-dropdown">
        <div className="shell-user-dropdown-header">
          <span className="shell-avatar" aria-hidden="true">
            {user.initials}
          </span>
          <div className="shell-user-info">
            <span className="shell-user-name" title={user.name}>
              {user.name}
            </span>
            <span className="shell-user-desc" title={summary}>
              {summary}
            </span>
          </div>
        </div>
        <Menu.Divider />
        <Menu.Item
          component={Link}
          to="/personalisation"
          leftSection={<IconSparkles size={16} stroke={1.75} aria-hidden="true" />}
          onClick={closeDrawer}
        >
          Personalisation
        </Menu.Item>
        <Menu.Item
          component={Link}
          to="/security"
          leftSection={<IconShieldCheck size={16} stroke={1.75} aria-hidden="true" />}
          onClick={closeDrawer}
        >
          Security
        </Menu.Item>
        <Menu.Item
          component={Link}
          to="/tokens"
          leftSection={<IconKey size={16} stroke={1.75} aria-hidden="true" />}
          onClick={closeDrawer}
        >
          API tokens
        </Menu.Item>
        <Menu.Divider />
        <Menu.Item
          color="red"
          className="shell-menu-logout"
          leftSection={<IconLogout size={16} stroke={1.75} aria-hidden="true" />}
          onClick={async () => {
            closeDrawer();
            await endThisSession();
            queryClient.clear();
            await navigate({ to: "/login" });
          }}
        >
          Log out
        </Menu.Item>
      </Menu.Dropdown>
    </Menu>
  );

  if (rail) {
    return (
      <>
        <IconButton
          aria-label="Toggle colour scheme"
          size="lg"
          tooltipPosition="right"
          onClick={() => setChoice(resolvedTheme === "dark" ? "light" : "dark")}
        >
          {resolvedTheme === "dark" ? (
            <IconSun size={16} stroke={1.75} aria-hidden="true" />
          ) : (
            <IconMoon size={16} stroke={1.75} aria-hidden="true" />
          )}
        </IconButton>
        {account}
      </>
    );
  }

  return (
    <>
      {account}
      <ThemeSwitch />
      <Text size="xs" ta="center" c="dimmed" ff="monospace">
        v0.1.0
      </Text>
    </>
  );
}

export function Shell() {
  const api = useApi();
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  const [collapsed, setCollapsed] = useState(false);
  const [drawerOpened, setDrawerOpened] = useState(false);
  const [dismissedAt, setDismissedAt] = useState<number>(0);

  const fillMain = fillsMain(pathname);

  const theme = useMantineTheme();
  const wide = useMediaQuery(`(min-width: ${theme.breakpoints.md})`, true, {
    getInitialValueInEffect: false,
  });

  // Ctrl+B collapses the rail from md and toggles the navigation drawer below it.
  const toggleSidebar = useCallback(() => {
    if (wide) setCollapsed((val) => !val);
    else setDrawerOpened((val) => !val);
  }, [wide]);

  // A route change closes the drawer, whether or not a nav item was clicked.
  const [drawerPath, setDrawerPath] = useState(pathname);
  if (drawerPath !== pathname) {
    setDrawerPath(pathname);
    setDrawerOpened(false);
  }

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
  const user: ShellUser = {
    name: userName,
    initials:
      userName
        .split(" ")
        .map((w) => w[0])
        .filter(Boolean)
        .join("")
        .slice(0, 2)
        .toUpperCase() || "U",
    roleName: userRole.charAt(0).toUpperCase() + userRole.slice(1),
    management: userRole === "owner" || userRole === "admin",
  };

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
    const handleKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "b") {
        event.preventDefault();
        toggleSidebar();
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [toggleSidebar]);

  return (
    <AppFrame
      className="shell"
      collapsed={collapsed}
      onCollapsedChange={setCollapsed}
      drawerOpened={drawerOpened}
      onDrawerOpenedChange={setDrawerOpened}
      fill={fillMain}
      mainClassName={fillMain ? "shell-main-fill" : undefined}
      header={<ShellHeader />}
      navigation={<ShellNavigation pathname={pathname} management={user.management} />}
      topBar={<ShellTopBar pathname={pathname} />}
      footer={<ShellFooter user={user} />}
    >
      {errorMessage ? (
        <StatusAlert
          tone="critical"
          className="shell-message"
          onDismiss={() => setDismissedAt(latestMutation.submittedAt)}
        >
          {errorMessage}
        </StatusAlert>
      ) : null}
      <PasskeyOffer />
      <Outlet />
    </AppFrame>
  );
}
