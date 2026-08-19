const money = new Intl.NumberFormat("en-GB", {
  style: "currency",
  currency: "USD",
});

const count = new Intl.NumberFormat("en-GB");

const percent = new Intl.NumberFormat("en-GB", {
  style: "percent",
  maximumFractionDigits: 0,
});

export function formatUsd(value: number | null): string {
  if (value === null) {
    return "Unknown";
  }
  return money.format(value);
}

export function formatTokens(value: number): string {
  return count.format(value);
}

export function formatRate(value: number): string {
  return percent.format(value);
}
