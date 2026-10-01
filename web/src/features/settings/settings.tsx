import { Outlet } from "@tanstack/react-router";

import { Section } from "../../ui/section";

import "../identity/identity.css";

export const settingsSections = [
  { to: "/personalisation", label: "Personalisation" },
  { to: "/security", label: "Security" },
  { to: "/tokens", label: "API tokens" },
  { to: "/connectors", label: "Connectors" },
] as const;

export const adminSections = [
  { to: "/admin/members", label: "Members" },
  { to: "/admin/teams", label: "Teams" },
  { to: "/admin/roles", label: "Roles" },
] as const;

export function SettingsLayout() {
  return <Outlet />;
}

export function AdminSettingsLayout() {
  return <Outlet />;
}

export function ConnectorsPage() {
  return (
    <Section
      title="Connectors"
      emptyTitle="Connect your tools"
      emptyBody="GitHub, email, calendar and any MCP server. Agents use only the connectors you add."
      emptyAction={{ label: "Add connector", to: "/connectors" }}
    />
  );
}
