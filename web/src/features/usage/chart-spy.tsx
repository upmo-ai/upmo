import type * as Components from "@upmo-ai/components";
import { createElement, type ReactElement } from "react";

/**
 * Test helper. The design-system charts draw with a lazily loaded ECharts and
 * jsdom has no layout, so a test cannot see the drawing. It sees the hidden data
 * table the chart carries and, through this spy, the props the page gave it:
 * series, orientation, stacking, formatters. The real chart still renders.
 */
type ChartProps = { title?: string };

const latest = new Map<string, ChartProps>();

function spy<P extends ChartProps>(Real: (props: P) => ReactElement): (props: P) => ReactElement {
  return function Spied(props: P) {
    latest.set(props.title ?? "", props);
    return createElement(Real, props);
  };
}

/** The design-system module with each multi-series chart wrapped to record its props. */
export function withChartSpy(actual: typeof Components): typeof Components {
  return {
    ...actual,
    LineChart: spy(actual.LineChart),
    GroupedBarChart: spy(actual.GroupedBarChart),
    DonutChart: spy(actual.DonutChart),
    ComboChart: spy(actual.ComboChart),
  } as typeof Components;
}

/** The props the chart with this title was last rendered with. */
export function chartProps<P extends ChartProps>(title: string): P {
  const props = latest.get(title);
  if (!props) {
    throw new Error(`no chart titled ${title} was rendered`);
  }
  return props as P;
}

export function resetChartProps(): void {
  latest.clear();
}
