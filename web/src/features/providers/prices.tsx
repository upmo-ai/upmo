import { Card, Group, Select, Stack, Text, TextInput, Title } from "@mantine/core";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { useState } from "react";
import { Badge, Button, Dialog, Input, StatusAlert, Table } from "@upmo-ai/components";

import {
  MEMBERS_KEY,
  PROVIDERS_KEY,
  providerPricingQueryKey,
  providersQueryKey,
  useApi,
  type MicroRates,
  type ModelPricing,
  type PriceRate,
  type PriceWarning,
} from "../../app/api";
import { formatDate } from "../identity/format";
import { formatMicro, microToUsdText, sourceLabel, usdToMicro } from "./models";
import { providerProblem } from "./rules";
import { fetchCanShare, type ListedProvider } from "./sharing";

const rateNames: ReadonlyArray<{ rate: PriceRate; label: string }> = [
  { rate: "input", label: "Input" },
  { rate: "output", label: "Output" },
  { rate: "cache_read", label: "Cache read" },
  { rate: "cache_write", label: "Cache write" },
];

type RateText = Record<PriceRate, string>;

/** The typed rates in micro-USD, or the sentence that says what is wrong with them. */
export function typedRates(text: RateText): { rates: MicroRates } | { problem: string } {
  const rates = { input: null, output: null, cache_read: null, cache_write: null } as MicroRates;
  for (const { rate, label } of rateNames) {
    const typed = text[rate].trim();
    if (typed === "") continue;
    const micro = usdToMicro(typed);
    if (micro === null) return { problem: `${label}: enter a price in USD, like 2.50, with at most six decimals.` };
    rates[rate] = micro;
  }
  if (rateNames.every(({ rate }) => rates[rate] === null)) return { problem: "Set at least one price." };
  return { rates };
}

const baseNames: Readonly<Record<string, string>> = {
  provider: "the provider's price",
  models_dev: "the models.dev price",
};

/** The 20% warning, as one sentence. */
function warningText(warning: PriceWarning): string {
  const label = rateNames.find(({ rate }) => rate === warning.rate)?.label ?? warning.rate;
  const base = baseNames[warning.base_source] ?? "the listed price";
  return `${label} ${formatMicro(warning.override)} is more than 20% away from ${base} of ${formatMicro(warning.base)}.`;
}

function PriceDialog({
  account,
  pricing,
  onClose,
}: {
  account: ListedProvider;
  pricing: ModelPricing;
  onClose: () => void;
}) {
  const api = useApi();
  const queryClient = useQueryClient();
  const start = (rate: PriceRate) => {
    const value = pricing.override?.[rate];
    return value == null ? "" : microToUsdText(value);
  };
  const [text, setText] = useState<RateText>({
    input: start("input"),
    output: start("output"),
    cache_read: start("cache_read"),
    cache_write: start("cache_write"),
  });
  const [problem, setProblem] = useState("");
  const key = providerPricingQueryKey(account.id, pricing.model);
  const done = (view: ModelPricing) => {
    queryClient.setQueryData(key, view);
    onClose();
  };

  // meta.shown: the dialog words a failure itself.
  const save = useMutation({
    meta: { shown: true },
    mutationFn: (rates: MicroRates) => api.providers.setPrice(account.id, pricing.model, rates),
    onSuccess: done,
    onError: (error: unknown) => setProblem(providerProblem(error)),
  });
  const clear = useMutation({
    meta: { shown: true },
    mutationFn: () => api.providers.clearPrice(account.id, pricing.model),
    onSuccess: done,
    onError: (error: unknown) => setProblem(providerProblem(error)),
  });
  const busy = save.isPending || clear.isPending;

  return (
    <Dialog opened onClose={onClose} title={`Your price for ${pricing.model}`}>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          setProblem("");
          const typed = typedRates(text);
          if ("problem" in typed) setProblem(typed.problem);
          else save.mutate(typed.rates);
        }}
      >
        <Stack gap="sm">
          <Text size="sm" c="dimmed">
            USD per million tokens. Leave a rate empty to keep the listed one.
          </Text>
          {rateNames.map(({ rate, label }) => (
            <Input
              key={rate}
              label={`${label} (USD per 1M tokens)`}
              inputMode="decimal"
              autoComplete="off"
              value={text[rate]}
              onChange={(event) => setText({ ...text, [rate]: event.target.value })}
            />
          ))}
          {problem ? (
            <StatusAlert tone="critical" title="Could not save the price">
              {problem}
            </StatusAlert>
          ) : null}
          <Group justify="space-between">
            {pricing.override ? (
              <Button variant="subtle" color="red" disabled={busy} loading={clear.isPending} onClick={() => clear.mutate()}>
                Remove your price
              </Button>
            ) : (
              <span />
            )}
            <Group gap="xs">
              <Button variant="subtle" disabled={busy} onClick={onClose}>
                Cancel
              </Button>
              <Button type="submit" disabled={busy} loading={save.isPending}>
                Save price
              </Button>
            </Group>
          </Group>
        </Stack>
      </form>
    </Dialog>
  );
}

function PriceRow({
  account,
  model,
  retired,
  canSet,
  names,
  onSet,
}: {
  account: ListedProvider;
  model: string;
  retired: boolean;
  canSet: boolean;
  names: ReadonlyMap<string, string>;
  onSet: (pricing: ModelPricing) => void;
}) {
  const api = useApi();
  const pricing = useQuery({
    queryKey: providerPricingQueryKey(account.id, model),
    queryFn: () => api.providers.pricing(account.id, model),
  });
  const view = pricing.data;
  const [pricingModel, setPricingModel] = useState(account.pricing_models?.[model] ?? "");
  const queryClient = useQueryClient();
  const mapMutation = useMutation({
    meta: { shown: true },
    mutationFn: () => api.providers.setPricingMap(account.id, undefined, { [model]: pricingModel.trim() || null }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: providersQueryKey() }),
  });
  const override = view?.override;
  const setBy = override?.updated_by ? (names.get(override.updated_by) ?? "a workspace admin") : undefined;

  return (
    <Table.Tr data-testid={`price-row-${model}`}>
      <Table.Td>
        <Stack gap={4}>
          <Text size="sm" ff="monospace">
            {model}
          </Text>
          {account.pricing_models?.[model] ? <Text size="xs" c="dimmed">Pricing model: {account.pricing_models[model]}</Text> : null}
          <Group gap={4}>
            {override ? (
              <Badge size="xs" color="teal" variant="light">
                Your price
              </Badge>
            ) : null}
            {retired ? (
              <Badge size="xs" color="orange" variant="light">
                Retired
              </Badge>
            ) : null}
            {view?.lifecycle && view.lifecycle !== "active" ? (
              <Badge size="xs" color="orange" variant="light">
                {view.retires_at ? `${view.lifecycle}, retires ${formatDate(view.retires_at)}` : view.lifecycle}
              </Badge>
            ) : null}
          </Group>
          {override ? (
            <Text size="xs" c="dimmed">
              Set{setBy ? ` by ${setBy}` : ""}
              {override.updated_at ? ` on ${formatDate(override.updated_at)}` : ""}
            </Text>
          ) : null}
          {view?.warnings.map((warning) => (
            <Text key={warning.rate} size="xs" c="orange" role="note">
              {warningText(warning)}
            </Text>
          ))}
        </Stack>
      </Table.Td>
      {rateNames.map(({ rate }) => (
        <Table.Td key={rate}>
          {pricing.isPending ? (
            <Text size="xs" c="dimmed">
              …
            </Text>
          ) : pricing.isError || !view ? (
            <Text size="xs" c="dimmed">
              Unavailable
            </Text>
          ) : (
            <Stack gap={2}>
              <Text size="sm">{view.rates[rate] === null ? "N/A" : formatMicro(view.rates[rate])}</Text>
              <Text size="xs" c="dimmed">
                {sourceLabel(view.sources[rate])}
              </Text>
            </Stack>
          )}
        </Table.Td>
      ))}
      {canSet ? (
        <Table.Td>
          <Stack gap="xs">
            <TextInput size="xs" aria-label={`Pricing model for ${model}`} value={pricingModel} placeholder={model} onChange={(event) => setPricingModel(event.currentTarget.value)} />
            <Button size="xs" variant="default" disabled={mapMutation.isPending} loading={mapMutation.isPending} aria-label={`Save mapping for ${model}`} onClick={() => mapMutation.mutate()}>
              Save mapping
            </Button>
            <Button
              size="xs"
              variant="default"
              disabled={!view}
              aria-label={`Set price for ${model}`}
              onClick={() => view && onSet(view)}
            >
              {override ? "Change" : "Set price"}
            </Button>
          </Stack>
        </Table.Td>
      ) : null}
    </Table.Tr>
  );
}

export function ProviderPricesPage() {
  const api = useApi();
  const { providerId } = useParams({ strict: false }) as { providerId?: string };
  const accounts = useQuery({ queryKey: providersQueryKey(), queryFn: () => api.providers.list() });
  // Setting a price needs settings:manage, the same permission sharing does.
  const canShare = useQuery({ queryKey: [...PROVIDERS_KEY, "sharing"], queryFn: fetchCanShare });
  const canSet = canShare.data === true;
  const members = useQuery({ queryKey: MEMBERS_KEY, queryFn: () => api.members.list(), enabled: canSet });
  const names = new Map((members.data ?? []).map((member) => [member.id, member.name]));
  const [editing, setEditing] = useState<ModelPricing | null>(null);
  const [pricingProviderDraft, setPricingProviderDraft] = useState<{ providerId: string; value: string }>();
  const vendors = useQuery({ queryKey: [...PROVIDERS_KEY, "vendors"], queryFn: () => api.providers.vendors() });
  const queryClient = useQueryClient();
  const saveProvider = useMutation({
    meta: { shown: true },
    mutationFn: () => api.providers.setPricingMap(providerId ?? "", pricingProvider, {}),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: providersQueryKey() }),
  });

  const account = accounts.data?.find((item) => item.id === providerId);
  const pricingProvider = pricingProviderDraft && pricingProviderDraft.providerId === providerId ? pricingProviderDraft.value : account?.pricing_provider ?? "";
  const back = (
    <div>
      <Button component={Link} to="/settings/providers" variant="default">
        Back to Providers
      </Button>
    </div>
  );

  if (accounts.isPending) {
    return (
      <Stack gap="md" className="identity-page">
        <Title order={1}>Prices</Title>
        <Text c="dimmed" size="sm" role="status">
          Loading prices
        </Text>
      </Stack>
    );
  }
  if (!account) {
    return (
      <Stack gap="md" className="identity-page">
        <Title order={1}>Provider not found</Title>
        <Text c="dimmed">{accounts.isError ? providerProblem(accounts.error) : "The requested provider could not be located."}</Text>
        {back}
      </Stack>
    );
  }

  const retired = new Set((account.enabled_models ?? []).filter((model) => model.retired).map((model) => model.id));
  return (
    <Stack gap="lg" className="identity-page">
      <Stack gap={4}>
        <Title order={1}>Prices for {account.name}</Title>
        <Text c="dimmed" size="sm">
          What each enabled model costs per million tokens, and where the price came from.
          {canSet ? " Set your own price where the listed one is wrong." : ""}
        </Text>
      </Stack>
      {canSet ? (
        <Card withBorder radius="lg" padding="md">
          <Group align="end">
            <Select label="Pricing provider" description="Use this vendor's models.dev prices for this account." aria-label="Pricing provider" searchable allowDeselect data={(vendors.data ?? []).map((vendor) => ({ value: vendor.key, label: vendor.name }))} value={pricingProvider || null} onChange={(value) => setPricingProviderDraft({ providerId: providerId ?? "", value: value ?? "" })} />
            {pricingProvider ? <Button variant="subtle" aria-label="Clear pricing provider" onClick={() => setPricingProviderDraft({ providerId: providerId ?? "", value: "" })}>Clear</Button> : null}
            <Button loading={saveProvider.isPending} disabled={saveProvider.isPending} onClick={() => saveProvider.mutate()}>Save pricing provider</Button>
          </Group>
        </Card>
      ) : null}
      <Card withBorder radius="lg" padding="lg">
        {account.models.length === 0 ? (
          <Text c="dimmed" size="sm">
            No models are enabled on this provider.
          </Text>
        ) : (
          <Table.ScrollContainer minWidth={640}>
            <Table verticalSpacing="sm">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Model</Table.Th>
                  {rateNames.map(({ rate, label }) => (
                    <Table.Th key={rate}>{label}</Table.Th>
                  ))}
                  {canSet ? <Table.Th>Your price</Table.Th> : null}
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {account.models.map((model) => (
                  <PriceRow
                    key={`${providerId}:${model}:${account.pricing_models?.[model] ?? ""}`}
                    account={account}
                    model={model}
                    retired={retired.has(model)}
                    canSet={canSet}
                    names={names}
                    onSet={setEditing}
                  />
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Card>
      {back}
      {editing ? <PriceDialog account={account} pricing={editing} onClose={() => setEditing(null)} /> : null}
    </Stack>
  );
}
