import { Stack, Text, Title } from "@mantine/core";

import { formatRate, formatTokens, formatUsd } from "./format";
import {
  cacheHitRate,
  knownSpend,
  periodCacheHit,
  sampleDays,
  sampleTasks,
  totalTokens,
  type Day,
  type TaskRow,
} from "./sample";

import "./usage.css";

function barStyle(value: number, max: number) {
  const pct = max <= 0 ? 0 : Math.round((value / max) * 100);
  return { width: `${pct}%` };
}

function SpendChart({ days }: { days: Day[] }) {
  const max = Math.max(0, ...days.map((day) => day.spendUsd ?? 0));
  return (
    <figure className="usage-chart" aria-label="Spend">
      <figcaption>Spend</figcaption>
      <table>
        <thead>
          <tr>
            <th scope="col">Day</th>
            <th scope="col">Amount</th>
          </tr>
        </thead>
        <tbody>
          {days.map((day) => (
            <tr key={day.label}>
              <th scope="row">{day.label}</th>
              <td>
                <span className="usage-bar-track" aria-hidden="true">
                  {day.spendUsd === null ? (
                    <span className="usage-bar usage-bar-unknown" />
                  ) : (
                    <span className="usage-bar" style={barStyle(day.spendUsd, max)} />
                  )}
                </span>
                {formatUsd(day.spendUsd)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </figure>
  );
}

function TokensChart({ tasks }: { tasks: TaskRow[] }) {
  const max = Math.max(0, ...tasks.map((task) => task.tokens));
  return (
    <figure className="usage-chart" aria-label="Tokens per task">
      <figcaption>Tokens per task</figcaption>
      <table>
        <thead>
          <tr>
            <th scope="col">Task</th>
            <th scope="col">Tokens</th>
          </tr>
        </thead>
        <tbody>
          {tasks.map((task) => (
            <tr key={task.title}>
              <th scope="row">{task.title}</th>
              <td>
                <span className="usage-bar-track" aria-hidden="true">
                  <span className="usage-bar" style={barStyle(task.tokens, max)} />
                </span>
                {formatTokens(task.tokens)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </figure>
  );
}

function CacheChart({ days }: { days: Day[] }) {
  const max = 1;
  return (
    <figure className="usage-chart" aria-label="Cache hit rate">
      <figcaption>Cache hit rate</figcaption>
      <table>
        <thead>
          <tr>
            <th scope="col">Day</th>
            <th scope="col">Hit rate</th>
          </tr>
        </thead>
        <tbody>
          {days.map((day) => {
            const rate = cacheHitRate(day.input, day.cacheRead);
            return (
              <tr key={day.label}>
                <th scope="row">{day.label}</th>
                <td>
                  <span className="usage-bar-track" aria-hidden="true">
                    <span className="usage-bar" style={barStyle(rate, max)} />
                  </span>
                  {formatRate(rate)}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </figure>
  );
}

export function UsagePage() {
  const spend = knownSpend(sampleDays);
  const tokens = totalTokens(sampleDays);
  const hit = periodCacheHit(sampleDays);

  return (
    <Stack gap="lg" className="usage-page">
      <div>
        <Title order={1}>Usage</Title>
        <Text c="dimmed">Last 7 days. Sample figures until the ledger exists.</Text>
      </div>
      <dl className="usage-figures">
        <div>
          <dt>Spend</dt>
          <dd>{formatUsd(spend)}</dd>
        </div>
        <div>
          <dt>Tokens</dt>
          <dd>{formatTokens(tokens)}</dd>
        </div>
        <div>
          <dt>Cache hit rate</dt>
          <dd>{formatRate(hit)}</dd>
        </div>
      </dl>
      <div className="usage-charts">
        <SpendChart days={sampleDays} />
        <TokensChart tasks={sampleTasks} />
        <CacheChart days={sampleDays} />
      </div>
    </Stack>
  );
}
