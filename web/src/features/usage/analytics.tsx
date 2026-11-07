import type { Usage } from "../../app/api";
import { ModelSpend } from "./model-spend";
import { ModelTokens } from "./model-tokens";
import { ProviderLatency } from "./provider-latency";
import { ProviderShare } from "./provider-share";
import { ProviderSpend } from "./provider-spend";
import { RequestsErrors } from "./requests-errors";
import { TokenConsumption } from "./token-consumption";

/** The analytics panels under the tiles. They are left out when the whole range has no usage. A range with tasks but no calls still shows each panel's own empty state. */
export function UsageAnalytics({ usage }: { usage: Usage }) {
  return (
    <div className="usage-analytics">
      <ProviderSpend usage={usage} />
      <ModelSpend usage={usage} />
      <ModelTokens usage={usage} />
      <TokenConsumption usage={usage} />
      <div className="usage-pair">
        <ProviderLatency usage={usage} />
        <ProviderShare usage={usage} />
      </div>
      <RequestsErrors usage={usage} />
    </div>
  );
}
