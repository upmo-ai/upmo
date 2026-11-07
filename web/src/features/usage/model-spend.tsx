import type { Usage } from "../../app/api";
import { SpendLines } from "./spend-lines";
import { hasCalls, modelSpendSeries, unpricedModels } from "./series";

export function ModelSpend({ usage }: { usage: Usage }) {
  const series = modelSpendSeries(usage);
  const empty = !hasCalls(usage) || series.length === 0;
  return (
    <SpendLines
      title="Spend by model"
      series={series}
      unpriced={unpricedModels(usage)}
      empty={empty ? "No spend by model in this range." : undefined}
    />
  );
}
