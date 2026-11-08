import type { Usage } from "../../app/api";
import { CacheHitChart } from "./cache-hit-chart";
import { ModelSpend } from "./model-spend";
import { ModelTokens } from "./model-tokens";
import { ProviderLatency } from "./provider-latency";
import { ProviderShare } from "./provider-share";
import { ProviderSpend } from "./provider-spend";
import { RequestsErrors } from "./requests-errors";
import { SpendChart } from "./spend-chart";
import { TaskCostChart } from "./task-cost-chart";
import { TokenConsumption } from "./token-consumption";

/**
 * Every chart panel under the tiles, in reading order. They sit two to a row on
 * a wide page and in one column on a narrow one; the two wide panels take a row
 * to themselves. They are left out when the whole range has no usage. A range
 * with tasks but no calls still shows each panel's own empty state.
 */
export function UsagePanels({ usage }: { usage: Usage }) {
  return (
    <div className="usage-panels">
      <SpendChart usage={usage} />
      <CacheHitChart usage={usage} />
      <ProviderSpend usage={usage} />
      <ModelSpend usage={usage} />
      <ModelTokens usage={usage} />
      <TokenConsumption usage={usage} />
      <ProviderLatency usage={usage} />
      <ProviderShare usage={usage} />
      <RequestsErrors usage={usage} />
      <TaskCostChart usage={usage} />
    </div>
  );
}
