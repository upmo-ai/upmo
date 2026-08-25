import { Anchor, Stack } from "@mantine/core";
import { Link, Outlet, useRouterState } from "@tanstack/react-router";

import { Section } from "../../ui/section";

import "../identity/identity.css";

export const settingsSections = [
  { to: "/settings/personalisation", label: "Personalisation" },
  { to: "/settings", label: "Security" },
  { to: "/settings/members", label: "Members" },
  { to: "/settings/tokens", label: "API tokens" },
  { to: "/settings/connectors", label: "Connectors" },
] as const;

export function SettingsLayout() {
  const pathname = useRouterState({ select: (state) => state.location.pathname });

  return (
    <Stack gap="lg" className="settings-page">
      <nav className="settings-nav" aria-label="Settings">
        {settingsSections.map((item) => {
          const current = pathname === item.to;
          return (
            <Anchor
              key={item.to}
              className={current ? "settings-nav-link settings-nav-current" : "settings-nav-link"}
              component={Link}
              to={item.to}
              activeOptions={{ exact: true }}
              aria-current={current ? "page" : undefined}
            >
              {item.label}
            </Anchor>
          );
        })}
      </nav>
      <Outlet />
    </Stack>
  );
}

export function ConnectorsPage() {
  return (
    <Section
      title="Connectors"
      emptyTitle="Connect your tools"
      emptyBody="GitHub, email, calendar and any MCP server. Agents use only the connectors you add."
      emptyAction={{ label: "Add connector", to: "/settings/connectors" }}
    />
  );
}
