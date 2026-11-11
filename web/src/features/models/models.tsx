import { Group, Popover, Stack, Text, Title } from "@mantine/core";
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
  decisionsQueryKey,
  gatewayRoutesQueryKey,
  useApi,
  type DecisionBinding,
  type DecisionGate,
  type GatewayRoute,
  type Scope,
} from "../../app/api";
import { ShellIcon } from "../../ui/icons";
import { decisionRows, formatUpdated, gatePercent, meetsBar, shortFingerprint, type DecisionRow } from "./rules";

import "./models.css";

function sentence(word: string): string {
  return word === "" ? word : word.charAt(0).toUpperCase() + word.slice(1);
}

/** Global rows are read-only for everyone. */
function ScopeBadge({ scope }: { scope: Scope }) {
  return (
    <Badge variant="light" size="sm" radius="xl">
      {scope === "global" ? "Global" : "Own"}
    </Badge>
  );
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
    {
      key: "name",
      header: "Name",
      mono: true,
      render: (route) => (
        <Group gap="xs" wrap="nowrap">
          {route.name}
          {route.usable ? null : (
            <Badge variant="light" size="sm">
              Inactive
            </Badge>
          )}
        </Group>
      ),
    },
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
    { key: "scope", header: "Scope", render: (route) => <ScopeBadge scope={route.scope} /> },
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

function Missing({ children }: { children: string }) {
  return (
    <Text size="sm" c="dimmed">
      {children}
    </Text>
  );
}

function BoundModel({ binding }: { binding: DecisionBinding | null }) {
  if (binding === null) return <Missing>No model bound</Missing>;
  // A route can carry the decision without a model of its own.
  const route =
    binding.route !== null && (binding.model === null || binding.route !== binding.decision) ? binding.route : null;
  if (binding.model === null && route === null) return <Missing>No model bound</Missing>;
  return (
    <Stack gap={0}>
      {binding.model === null ? (
        <Missing>No model bound</Missing>
      ) : (
        <Text size="sm" ff="monospace">
          {binding.model}
        </Text>
      )}
      {route !== null ? (
        <Text size="xs" c="dimmed">{`via route ${route}`}</Text>
      ) : null}
    </Stack>
  );
}

function LastEval({ binding }: { binding: DecisionBinding | null }) {
  // A gate with no decision bound to it has no eval to report, so say so
  // rather than claim it was never evaluated.
  if (binding === null) return <Missing>No decision bound</Missing>;
  const last = binding.last_eval;
  if (last === null) return <Missing>Never evaluated</Missing>;
  const when = formatUpdated(last.at);
  return (
    <Text size="sm">
      {sentence(last.verdict)}
      {", "}
      {when === "" ? "date unknown" : <time dateTime={last.at}>{when}</time>}
    </Text>
  );
}

function GatesTable({
  rows,
  gatesLoaded,
  bindingsLoaded,
}: {
  rows: DecisionRow[];
  gatesLoaded: boolean;
  bindingsLoaded: boolean;
}) {
  // A gate that failed to load is not the same as a decision with no gate.
  const noGate = (fallback: string) => <Missing>{gatesLoaded ? fallback : "Not loaded"}</Missing>;
  const columns: DataTableColumn<DecisionRow>[] = [
    { key: "decision", header: "Decision", mono: true, render: (row) => row.decision },
    {
      key: "kind",
      header: "Kind",
      render: (row) => (row.gate ? row.gate.kind : noGate("None")),
    },
    {
      key: "fingerprint",
      header: "Fingerprint",
      mono: true,
      render: (row) => (row.gate ? <Fingerprint gate={row.gate} /> : noGate("None")),
    },
    {
      key: "verdict",
      header: "Verdict",
      render: (row) => (row.gate ? sentence(row.gate.verdict) : noGate("No gate yet")),
    },
    {
      key: "score",
      header: "Score",
      numeric: true,
      render: (row) =>
        row.gate ? `${row.gate.hit} / ${row.gate.total} (${gatePercent(row.gate)}%)` : noGate("None"),
    },
    {
      key: "bar",
      header: "Bar",
      numeric: true,
      render: (row) => (row.gate ? `${row.gate.bar}%` : noGate("None")),
    },
    {
      key: "updated",
      header: "Updated",
      render: (row) =>
        row.gate ? (
          <time dateTime={row.gate.updated_at}>{formatUpdated(row.gate.updated_at)}</time>
        ) : (
          noGate("None")
        ),
    },
    {
      key: "against",
      header: "Against the bar",
      render: (row) => (row.gate ? <BarBadge gate={row.gate} /> : noGate("None")),
    },
    {
      key: "model",
      header: "Bound model",
      render: (row) => (bindingsLoaded ? <BoundModel binding={row.binding} /> : <Missing>Not loaded</Missing>),
    },
    {
      key: "last-eval",
      header: "Last eval",
      render: (row) => (bindingsLoaded ? <LastEval binding={row.binding} /> : <Missing>Not loaded</Missing>),
    },
    { key: "scope", header: "Scope", render: (row) => <ScopeBadge scope={row.scope} /> },
  ];

  return (
    <DataTable
      aria-label="Decision gates"
      caption="Each decision with the model it is bound to and its last eval. A decide route changes model only when its gate reaches the bar."
      minWidth={1260}
      columns={columns}
      rows={rows}
      rowKey={(row) => row.key}
    />
  );
}

export function ModelsPage() {
  const api = useApi();

  const routes = useQuery({
    queryKey: gatewayRoutesQueryKey(),
    queryFn: () => api.gateway.routes(),
  });
  const gates = useQuery({
    queryKey: decisionGatesQueryKey(),
    queryFn: () => api.decisions.gates(),
  });
  const decisions = useQuery({
    queryKey: decisionsQueryKey(),
    queryFn: () => api.decisions.list(),
  });

  const rows = decisionRows(decisions.data ?? [], gates.data ?? []);

  return (
    <Stack gap="lg" className="models-page">
      <PageHeader
        title="Models and routes"
        description="How Upmo reaches models, and whether each decision gate holds its bar. Read only."
      />

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
        {gates.isError ? <StatusAlert tone="critical">Decision gates could not be loaded.</StatusAlert> : null}
        {decisions.isError ? (
          <StatusAlert tone="critical">Decision bindings could not be loaded.</StatusAlert>
        ) : null}
        {gates.isPending || decisions.isPending ? (
          <Text c="dimmed" role="status">
            Loading decision gates
          </Text>
        ) : rows.length > 0 ? (
          <GatesTable rows={rows} gatesLoaded={!gates.isError} bindingsLoaded={!decisions.isError} />
        ) : gates.isError || decisions.isError ? null : (
          <EmptyState
            title="No decision gates yet"
            description="A gate shows up here once a decide route passes its labelled test."
            icon={<ShellIcon concept="nav.settings" size={24} />}
          />
        )}
      </Stack>
    </Stack>
  );
}
