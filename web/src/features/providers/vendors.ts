import type { ProviderPlanKind, ProviderVendor } from "../../app/api";

/** A vendor as the picker shows it, read from GET /api/providers/vendors. */
export interface PickerVendor {
  id: string;
  name: string;
  adapter: string;
  description: string;
  suggestedPlan: ProviderPlanKind;
  /** Whether the vendor takes a key. A local runtime may run without one. */
  requiresKey: boolean;
  /** The address the brain reaches the vendor at when the person gives none. */
  defaultBaseUrl?: string;
}

/**
 * A provider with no network behind it, for trying the screens and the e2e
 * checks. It is not one of the brain's vendors; it is offered in a dev build
 * only.
 */
export const TEST_VENDOR: PickerVendor = {
  id: "fake",
  name: "Test provider",
  adapter: "fake",
  description: "Built in, no network. For trying Upmo out.",
  suggestedPlan: "local",
  requiresKey: true,
};

const planWords: Readonly<Record<ProviderPlanKind, string>> = {
  metered: "Pay per token",
  subscription: "Subscription",
  local: "Runs on your machine",
};

/** The brain's vendor as a picker tile. A vendor with no suggested plan is metered. */
export function pickerVendor(vendor: ProviderVendor): PickerVendor {
  const plan = vendor.suggested_plan ?? "metered";
  return {
    id: vendor.key,
    name: vendor.name,
    adapter: vendor.adapter,
    description: planWords[plan],
    suggestedPlan: plan,
    requiresKey: plan !== "local",
    defaultBaseUrl: vendor.default_base_url || undefined,
  };
}

/** The tiles the picker offers: the brain's vendors, and the test provider in a dev build. */
export function pickerVendors(vendors: readonly ProviderVendor[], dev: boolean): PickerVendor[] {
  const out = vendors.map(pickerVendor);
  return dev ? [...out, TEST_VENDOR] : out;
}

/** One line under each plan choice. */
export const planOptions: ReadonlyArray<{ value: ProviderPlanKind; label: string; line: string }> = [
  { value: "metered", label: "Metered", line: "Pay as you go, priced per token." },
  { value: "subscription", label: "Subscription", line: "A flat plan you pay the provider for." },
  { value: "local", label: "Local", line: "Self-hosted. A key is optional." },
];
