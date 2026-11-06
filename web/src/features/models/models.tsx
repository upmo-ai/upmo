import { Popover, Stack, Text, Title } from "@mantine/core";
import { IconCheck, IconX } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import { useId, useState } from "react";
import {
  Badge,
  Button,
  DataTable,
  EmptyState,
  PageHeader,
  StatusAlert,
  Table,
  type DataTableColumn,
} from "@upmo-ai/components";

import {
  decisionGatesQueryKey,
  gatewayRoutesQueryKey,
  sessionQueryKey,
  useApi,
  type DecisionGate,
  type GatewayRoute,
} from "../../app/api";
import { ShellIcon } from "../../ui/icons";
import {
  canManageSettings,
  formatUpdated,
  gatePercent,
  meetsBar,
  needsSettingsAccessBody,
  needsSettingsAccessTitle,
  shortFingerprint,
} from "./rules";

import "./models.css";

/** The server refuses with 401 or 403 when the person lacks settings:manage. */
function isAccessRefusal(error: unknown): boolean {
  if (!error || typeof error !== "object" || !("status" in error)) return false;
  const status = (error as { status: unknown }).status;
  return status === 401 || status === 403;
}

function sentence(word: string): string {
  return word === "" ? word : word.charAt(0).toUpperCase() + word.slice(1);
}

function HopsCell({
  route,
  open,
  onToggle,
}: {
  route: GatewayRoute;
  open: boolean;
  onToggle: () => void;
}) {
  const panel = useId();
  const count = route.hops.length;
  if (count === 0) {
    return (
      <Text size="sm" c="dimmed">
        No hops
      </Text>
    );
  }
  const hops = [...route.hops].sort((a, b) => a.position - b.position);
  // The visible text leads the accessible name, which adds the route.
  const label = `${open ? "Hide" : "Show"} ${count} ${count === 1 ? "hop" : "hops"}`;
  return (
    <Stack gap="xs" align="flex-start">
      <Button
        variant="subtle"
        size="compact-sm"
        aria-label={`${label} for ${route.name}`}
        aria-expanded={open}
        aria-controls={panel}
        onClick={onToggle}
      >
        {label}
      </Button>
      <div id={panel} hidden={!open} className="models-hops">
        {open ? (
          <Table aria-label={`Hops for ${route.name}`} highlightOnHover={false} stickyHeader={false}>
            <Table.Thead>
              <Table.Tr>
                <Table.Th ta="right">Position</Table.Th>
                <Table.Th>Adapter</Table.Th>
                <Table.Th>Model</Table.Th>
                <Table.Th ta="right">Weight</Table.Th>
                <Table.Th ta="right">Retry</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {hops.map((hop) => (
                <Table.Tr key={hop.position}>
                  <Table.Num>{hop.position}</Table.Num>
                  <Table.Mono>{hop.adapter}</Table.Mono>
                  <Table.Mono>{hop.model}</Table.Mono>
                  <Table.Num>{hop.weight}</Table.Num>
                  <Table.Num>{hop.retry}</Table.Num>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        ) : null}
      </div>
    </Stack>
  );
}

function RoutesTable({ routes }: { routes: GatewayRoute[] }) {
  const [opened, setOpened] = useState<ReadonlySet<string>>(new Set());

  function toggle(name: string) {
    setOpened((current) => {
      const next = new Set(current);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  }

  const columns: DataTableColumn<GatewayRoute>[] = [
    { key: "name", header: "Name", mono: true, render: (route) => route.name },
    {
      key: "capability",
      header: "Capability",
      render: (route) => (
        <Badge variant="light" size="sm" radius="xl">
          {route.capability}
        </Badge>
      ),
    },
    {
      key: "decision",
      header: "Decision",
      render: (route) =>
        route.decision === "" ? (
          <Text size="sm" c="dimmed">
            None
          </Text>
        ) : (
          <Badge variant="light" size="sm" radius="xl">
            {route.decision}
          </Badge>
        ),
    },
    {
      key: "hops",
      header: "Hops",
      render: (route) => (
        <HopsCell route={route} open={opened.has(route.name)} onToggle={() => toggle(route.name)} />
      ),
    },
  ];

  return (
    <DataTable
      aria-label="Routes"
      caption="Each route tries its hops in order. Open a route to see them."
      minWidth={640}
      columns={columns}
      rows={routes}
      rowKey={(route) => route.name}
    />
  );
}

function BarBadge({ gate }: { gate: DecisionGate }) {
  const met = meetsBar(gate);
  return (
    <Badge
      variant="light"
      size="sm"
      radius="xl"
      className={met ? "models-bar-met" : "models-bar-missed"}
      leftSection={
        met ? (
          <IconCheck size={12} stroke={2.2} aria-hidden="true" />
        ) : (
          <IconX size={12} stroke={2.2} aria-hidden="true" />
        )
      }
    >
      {met ? "Meets bar" : "Below bar"}
    </Badge>
  );
}

/**
 * The short form is a button, so the full fingerprint opens on a keypress or a
 * tap as well as a click, and its text can be selected and copied.
 */
function Fingerprint({ gate }: { gate: DecisionGate }) {
  const short = shortFingerprint(gate.fingerprint);
  const labelId = useId();
  return (
    <Popover position="bottom-start" shadow="md" withinPortal closeOnEscape returnFocus>
      <Popover.Target>
        <Button
          variant="subtle"
          size="compact-sm"
          className="models-fingerprint"
          aria-label={`${short}, show full fingerprint for ${gate.route}`}
        >
          {short}
        </Button>
      </Popover.Target>
      <Popover.Dropdown aria-labelledby={labelId} className="models-fingerprint-full">
        <Text size="xs" c="dimmed" id={labelId}>
          {`Full fingerprint for ${gate.route}`}
        </Text>
        <Text size="sm" ff="monospace" className="models-fingerprint-text">
          {gate.fingerprint}
        </Text>
      </Popover.Dropdown>
    </Popover>
  );
}

function GatesTable({ gates }: { gates: DecisionGate[] }) {
  const columns: DataTableColumn<DecisionGate>[] = [
    { key: "route", header: "Route", mono: true, render: (gate) => gate.route },
    { key: "kind", header: "Kind", render: (gate) => gate.kind },
    {
      key: "fingerprint",
      header: "Fingerprint",
      mono: true,
      render: (gate) => <Fingerprint gate={gate} />,
    },
    { key: "verdict", header: "Verdict", render: (gate) => sentence(gate.verdict) },
    {
      key: "score",
      header: "Score",
      numeric: true,
      render: (gate) => `${gate.hit} / ${gate.total} (${gatePercent(gate)}%)`,
    },
    { key: "bar", header: "Bar", numeric: true, render: (gate) => `${gate.bar}%` },
    {
      key: "updated",
      header: "Updated",
      render: (gate) => <time dateTime={gate.updated_at}>{formatUpdated(gate.updated_at)}</time>,
    },
    { key: "against", header: "Against the bar", render: (gate) => <BarBadge gate={gate} /> },
  ];

  return (
    <DataTable
      aria-label="Decision gates"
      caption="A decide route changes model only when its gate reaches the bar."
      minWidth={920}
      columns={columns}
      rows={gates}
      rowKey={(gate) => gate.route}
    />
  );
}

export function ModelsPage() {
  const api = useApi();
  const session = useQuery({ queryKey: sessionQueryKey(), queryFn: () => api.session.get() });
  const allowed = session.data ? canManageSettings(session.data.view) : false;

  const routes = useQuery({
    queryKey: gatewayRoutesQueryKey(),
    queryFn: () => api.gateway.routes(),
    enabled: allowed,
  });
  const gates = useQuery({
    queryKey: decisionGatesQueryKey(),
    queryFn: () => api.decisions.gates(),
    enabled: allowed,
  });

  const header = (
    <PageHeader
      title="Models and routes"
      description="How Upmo reaches models, and whether each decision gate holds its bar. Read only."
    />
  );

  if (session.isPending) {
    return (
      <Stack gap="lg" className="models-page">
        {header}
        <Text c="dimmed" role="status">
          Loading
        </Text>
      </Stack>
    );
  }

  const refused = !allowed || isAccessRefusal(routes.error) || isAccessRefusal(gates.error);
  if (refused) {
    return (
      <Stack gap="lg" className="models-page">
        {header}
        <EmptyState
          title={needsSettingsAccessTitle}
          description={needsSettingsAccessBody}
          icon={<ShellIcon concept="nav.settings" size={24} />}
          headingLevel={2}
        />
      </Stack>
    );
  }

  return (
    <Stack gap="lg" className="models-page">
      {header}

      <Stack gap="sm" component="section" aria-labelledby="models-routes-heading">
        <Title order={2} id="models-routes-heading">
          Routes
        </Title>
        {routes.isError ? (
          <StatusAlert tone="critical">Routes could not be loaded.</StatusAlert>
        ) : routes.isPending ? (
          <Text c="dimmed" role="status">
            Loading routes
          </Text>
        ) : routes.data.length === 0 ? (
          <EmptyState
            title="No routes yet"
            description="Routes show up here once the gateway has been set up."
            icon={<ShellIcon concept="nav.settings" size={24} />}
          />
        ) : (
          <RoutesTable routes={routes.data} />
        )}
      </Stack>

      <Stack gap="sm" component="section" aria-labelledby="models-gates-heading">
        <Title order={2} id="models-gates-heading">
          Decision gates
        </Title>
        {gates.isError ? (
          <StatusAlert tone="critical">Decision gates could not be loaded.</StatusAlert>
        ) : gates.isPending ? (
          <Text c="dimmed" role="status">
            Loading decision gates
          </Text>
        ) : gates.data.length === 0 ? (
          <EmptyState
            title="No decision gates yet"
            description="A gate shows up here once a decide route passes its labelled test."
            icon={<ShellIcon concept="nav.settings" size={24} />}
          />
        ) : (
          <GatesTable gates={gates.data} />
        )}
      </Stack>
    </Stack>
  );
}
