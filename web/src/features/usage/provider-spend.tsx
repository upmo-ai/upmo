import type { Usage } from "../../app/api";
import { SpendLines } from "./spend-lines";
import { hasCalls, providerSpendSeries, unpricedProviders } from "./series";

export function ProviderSpend({ usage }: { usage: Usage }) {
  const series = providerSpendSeries(usage);
  const empty = !hasCalls(usage) || series.length === 0;
  return (
    <SpendLines
      title="Spend by provider"
      series={series}
      unpriced={unpricedProviders(usage)}
      empty={empty ? "No spend by provider in this range." : undefined}
    />
  );
}
