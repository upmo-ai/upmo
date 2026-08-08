import {
  Outlet,
  createBrowserHistory,
  createRootRoute,
  createRoute,
  createRouter,
  type RouterHistory,
} from "@tanstack/react-router";

import { MembersPage } from "../features/identity/members";
import { SecurityPage } from "../features/identity/security";
import { TokensPage } from "../features/identity/tokens";
import { ConnectorsPage, SettingsLayout } from "../features/settings/settings";
import { TaskPage, TasksPage } from "../features/tasks/tasks";
import { PrivacyPage, TermsPage } from "./legal";
import { LinkPage } from "./link";
import { SignInPage } from "./sign-in";
import { Shell } from "./shell";
import {
  AgentsPage,
  AutomationsPage,
  ChatPage,
  HomePage,
  KnowledgePage,
  UsagePage,
} from "./pages";

const rootRoute = createRootRoute({
  component: () => <Outlet />,
});

const signInRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  component: SignInPage,
});

const linkRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login/link",
  component: LinkPage,
});

const termsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/terms",
  component: TermsPage,
});

const privacyRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/privacy",
  component: PrivacyPage,
});

const shellRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "shell",
  component: Shell,
});

const homeRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/",
  component: HomePage,
});

const chatRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/chat",
  component: ChatPage,
});

const tasksRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/tasks",
  component: () => <Outlet />,
});

const tasksIndexRoute = createRoute({
  getParentRoute: () => tasksRoute,
  path: "/",
  component: TasksPage,
});

const taskRoute = createRoute({
  getParentRoute: () => tasksRoute,
  path: "$taskId",
  component: TaskPage,
});

const automationsRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/automations",
  component: AutomationsPage,
});

const agentsRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/agents",
  component: AgentsPage,
});

const knowledgeRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/knowledge",
  component: KnowledgePage,
});

const usageRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/usage",
  component: UsagePage,
});

const settingsRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/settings",
  component: SettingsLayout,
});

const settingsIndexRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "/",
  component: SecurityPage,
});

const membersRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "members",
  component: MembersPage,
});

const tokensRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "tokens",
  component: TokensPage,
});

const connectorsRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "connectors",
  component: ConnectorsPage,
});

const routeTree = rootRoute.addChildren([
  signInRoute,
  linkRoute,
  termsRoute,
  privacyRoute,
  shellRoute.addChildren([
    homeRoute,
    chatRoute,
    tasksRoute.addChildren([tasksIndexRoute, taskRoute]),
    automationsRoute,
    agentsRoute,
    knowledgeRoute,
    usageRoute,
    settingsRoute.addChildren([settingsIndexRoute, membersRoute, tokensRoute, connectorsRoute]),
  ]),
]);

export function createAppRouter(history?: RouterHistory) {
  return createRouter({
    routeTree,
    history: history ?? createBrowserHistory(),
  });
}

export type { RouterHistory };

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof createAppRouter>;
  }
}
