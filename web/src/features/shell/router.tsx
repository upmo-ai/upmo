import {
  Outlet,
  createBrowserHistory,
  createRootRoute,
  createRoute,
  createRouter,
  type RouterHistory,
} from "@tanstack/react-router";

import {
  AgentsPage,
  AutomationsPage,
  ChatPage,
  ConnectorsPage,
  HomePage,
  KnowledgePage,
  SettingsPage,
  TasksPage,
  UsagePage,
} from "./pages";
import { Shell } from "./shell";

const rootRoute = createRootRoute({
  component: Shell,
});

const homeRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  component: HomePage,
});

const chatRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/chat",
  component: ChatPage,
});

const tasksRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/tasks",
  component: TasksPage,
});

const automationsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/automations",
  component: AutomationsPage,
});

const agentsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/agents",
  component: AgentsPage,
});

const knowledgeRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/knowledge",
  component: KnowledgePage,
});

const usageRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/usage",
  component: UsagePage,
});

const settingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/settings",
  component: () => <Outlet />,
});

const settingsIndexRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "/",
  component: SettingsPage,
});

const connectorsRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "connectors",
  component: ConnectorsPage,
});

const routeTree = rootRoute.addChildren([
  homeRoute,
  chatRoute,
  tasksRoute,
  automationsRoute,
  agentsRoute,
  knowledgeRoute,
  usageRoute,
  settingsRoute.addChildren([settingsIndexRoute, connectorsRoute]),
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
