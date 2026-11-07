import { ComboChart } from "@upmo-ai/components";

import type { Usage } from "../../app/api";
import { AnalyticsPanel } from "./analytics-panel";
import { errorRate } from "./figures";
import { formatBucket, formatRatePrecise, formatTokens } from "./format";
import { hasCalls } from "./series";

export function RequestsErrors({ usage }: { usage: Usage }) {
  const { buckets, bucket: unit } = usage;
  const empty = !hasCalls(usage);
  return (
    <AnalyticsPanel title="Requests and error rate" empty={empty ? "No requests in this range." : undefined}>
      <ComboChart
        title="Requests and error rate"
        formatBar={formatTokens}
        formatLine={(percent) => formatRatePrecise(percent / 100)}
        bars={{
          name: "Requests",
          points: buckets.map((bucket) => ({ label: formatBucket(bucket.at, unit), value: bucket.requests })),
        }}
        line={{
          name: "Error rate",
          points: buckets.map((bucket) => ({
            label: formatBucket(bucket.at, unit),
            value: errorRate(bucket.requests, bucket.errors) * 100,
          })),
        }}
      />
    </AnalyticsPanel>
  );
}
