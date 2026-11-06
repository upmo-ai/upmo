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
import { RolesPage } from "../features/identity/roles";
import { TeamsPage } from "../features/identity/teams";
import { SecurityPage } from "../features/identity/security";
import { TokensPage } from "../features/identity/tokens";
import { AdminSettingsLayout } from "../features/settings/settings";
import { ConnectorsPage } from "../features/connectors/connectors";
import { EditConnectorPage, NewConnectorPage } from "../features/connectors/connector-form";
import { ProvidersPage } from "../features/providers/providers";
import { EditProviderPage, NewProviderPage } from "../features/providers/provider-form";
import { TaskPage, TasksPage } from "../features/tasks/tasks";
import { KnowledgeFolderPage } from "../features/knowledge/folder";
import { KnowledgePage } from "../features/knowledge/knowledge";
import { ModelsPage } from "../features/models/models";
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
  component: () => <Outlet />,
});

const knowledgeIndexRoute = createRoute({
  getParentRoute: () => knowledgeRoute,
  path: "/",
  component: KnowledgePage,
});

const knowledgeFolderRoute = createRoute({
  getParentRoute: () => knowledgeRoute,
  path: "$folderId",
  component: KnowledgeFolderPage,
});

const usageRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/usage",
  component: UsagePage,
});

const connectorsRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/connectors",
  component: () => <Outlet />,
});

const connectorsIndexRoute = createRoute({
  getParentRoute: () => connectorsRoute,
  path: "/",
  component: ConnectorsPage,
});

const newConnectorRoute = createRoute({
  getParentRoute: () => connectorsRoute,
  path: "new",
  component: NewConnectorPage,
});

const editConnectorRoute = createRoute({
  getParentRoute: () => connectorsRoute,
  path: "$connectorId/edit",
  component: EditConnectorPage,
});

const providersRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/providers",
  component: () => <Outlet />,
});

const providersIndexRoute = createRoute({
  getParentRoute: () => providersRoute,
  path: "/",
  component: ProvidersPage,
});

const newProviderRoute = createRoute({
  getParentRoute: () => providersRoute,
  path: "new",
  component: NewProviderPage,
});

const editProviderRoute = createRoute({
  getParentRoute: () => providersRoute,
  path: "$providerId/edit",
  component: EditProviderPage,
});

const personalisationRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/personalisation",
  component: PersonalisationPage,
});

const legacyPersonalizationRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/personalization",
  beforeLoad: () => {
    throw redirect({ to: "/personalisation" });
  },
});

const securityRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/security",
  component: SecurityPage,
});

const tokensRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/tokens",
  component: TokensPage,
});

const settingsRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/settings",
  component: () => <Outlet />,
});

const settingsIndexRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/security" });
  },
});

const settingsPersonalisationRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "personalization",
  beforeLoad: () => {
    throw redirect({ to: "/personalisation" });
  },
});

const settingsPersonalisationUKRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "personalisation",
  beforeLoad: () => {
    throw redirect({ to: "/personalisation" });
  },
});

const settingsTokensRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "tokens",
  beforeLoad: () => {
    throw redirect({ to: "/tokens" });
  },
});

const settingsConnectorsRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "connectors",
  beforeLoad: () => {
    throw redirect({ to: "/connectors" });
  },
});

const settingsProvidersRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "providers",
  beforeLoad: () => {
    throw redirect({ to: "/providers" });
  },
});

const settingsModelsRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "models",
  component: ModelsPage,
});

const adminRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/admin",
  component: AdminSettingsLayout,
});

const adminIndexRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/admin/members" });
  },
});

const adminMembersRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: "members",
  component: MembersPage,
});

const adminTeamsRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: "teams",
  component: TeamsPage,
});

const adminRolesRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: "roles",
  component: RolesPage,
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
    knowledgeRoute.addChildren([knowledgeIndexRoute, knowledgeFolderRoute]),
    usageRoute,
    connectorsRoute.addChildren([
      connectorsIndexRoute,
      newConnectorRoute,
      editConnectorRoute,
    ]),
    providersRoute.addChildren([
      providersIndexRoute,
      newProviderRoute,
      editProviderRoute,
    ]),
    personalisationRoute,
    legacyPersonalizationRoute,
    securityRoute,
    tokensRoute,
    settingsRoute.addChildren([
      settingsIndexRoute,
      settingsPersonalisationRoute,
      settingsPersonalisationUKRoute,
      settingsTokensRoute,
      settingsConnectorsRoute,
      settingsProvidersRoute,
      settingsModelsRoute,
    ]),
    adminRoute.addChildren([
      adminIndexRoute,
      adminMembersRoute,
      adminTeamsRoute,
      adminRolesRoute,
    ]),
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
