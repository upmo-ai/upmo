import { GroupedBarChart } from "@upmo-ai/components";

import type { Usage } from "../../app/api";
import { UsagePanel } from "./panel";
import { formatLatency } from "./format";
import { hasCalls, latencyProviders, providerLabel } from "./series";

export function ProviderLatency({ usage }: { usage: Usage }) {
  const providers = latencyProviders(usage);
  const empty = !hasCalls(usage) || providers.length === 0;
  return (
    <UsagePanel title="Latency per provider" empty={empty ? "No latency to show in this range." : undefined}>
      <GroupedBarChart
        title="Latency per provider"
        orientation="horizontal"
        formatValue={formatLatency}
        groups={providers.map((provider) => ({
          label: providerLabel(provider),
          bars: [
            { name: "p50", value: provider.latency_ms.p50 },
            { name: "p95", value: provider.latency_ms.p95 },
            { name: "p99", value: provider.latency_ms.p99 },
          ],
        }))}
      />
    </UsagePanel>
  );
}
