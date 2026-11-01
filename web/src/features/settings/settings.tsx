import { Outlet } from "@tanstack/react-router";

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

export function AdminSettingsLayout() {
  return <Outlet />;
}
