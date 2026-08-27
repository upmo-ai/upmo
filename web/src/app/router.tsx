import type { QueryClient } from "@tanstack/react-query";
import {
  Outlet,
  createBrowserHistory,
  createRootRouteWithContext,
  createRoute,
  createRouter,
  redirect,
  type RouterHistory,
} from "@tanstack/react-router";

import { BrainError, sessionQueryKey, type ApiClient } from "./api";
import { MembersPage } from "../features/identity/members";
import { SecurityPage } from "../features/identity/security";
import { TokensPage } from "../features/identity/tokens";
import { AdminSettingsLayout, ConnectorsPage, SettingsLayout } from "../features/settings/settings";
import { TaskPage, TasksPage } from "../features/tasks/tasks";
import { KnowledgePage } from "../features/knowledge/knowledge";
import { PersonalisationPage } from "../features/memory/memory";
import { UsagePage } from "../features/usage/usage";
import { PrivacyPage, TermsPage } from "./legal";
import { LinkPage } from "./link";
import { SignInPage } from "./sign-in";
import { Shell } from "./shell";
import { AgentsPage, AutomationsPage, ChatPage, HomePage } from "./pages";

export interface RouterContext {
  queryClient: QueryClient;
  api: ApiClient;
}

const rootRoute = createRootRouteWithContext<RouterContext>()({
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
  beforeLoad: async ({ context }) => {
    try {
      await context.queryClient.ensureQueryData({
        queryKey: sessionQueryKey(),
        queryFn: () => context.api.session.get(),
      });
    } catch (err) {
      if (
        (err instanceof BrainError && err.status === 401 && err.error === "unauthorised") ||
        (err && typeof err === "object" && (err as { status?: unknown }).status === 401 && (err as { error?: unknown }).error === "unauthorised")
      ) {
        throw redirect({ to: "/login" });
      }
      throw err;
    }
  },
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

const personalisationRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "personalization",
  component: PersonalisationPage,
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

const adminRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/admin",
  component: AdminSettingsLayout,
});

const adminIndexRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: "/",
  component: MembersPage,
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
    settingsRoute.addChildren([
      settingsIndexRoute,
      personalisationRoute,
      tokensRoute,
      connectorsRoute,
    ]),
    adminRoute.addChildren([adminIndexRoute]),
  ]),
]);

export function createAppRouter(history?: RouterHistory, context?: RouterContext) {
  return createRouter({
    routeTree,
    history: history ?? createBrowserHistory(),
    context: context ?? {
      queryClient: undefined as unknown as QueryClient,
      api: undefined as unknown as ApiClient,
    },
  });
}

export type { RouterHistory };

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof createAppRouter>;
  }
}
