import { Anchor } from "@mantine/core";
import { Link } from "@tanstack/react-router";

import { Section } from "../../ui/section";

export function SettingsPage() {
  return (
    <Section
      title="Settings"
      emptyTitle="Workspace settings"
      emptyBody="Connectors and other sections for this workspace live here."
    >
      <Anchor component={Link} to="/settings/connectors" fw={600}>
        Connectors
      </Anchor>
    </Section>
  );
}

export function ConnectorsPage() {
  return (
    <Section
      title="Connectors"
      emptyTitle="Connect your tools"
      emptyBody="GitHub, email, calendar and any MCP server. Agents use only the connectors you add."
    />
  );
}
