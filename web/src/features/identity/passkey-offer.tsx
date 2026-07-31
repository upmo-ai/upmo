import { Button, Text } from "@mantine/core";
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { meQueryKey, passkeysQueryKey, useApi } from "../../app/api";
import { showPasskeyOffer } from "./rules";

import "./identity.css";

export function PasskeyOffer() {
  const api = useApi();
  const queryClient = useQueryClient();
  const me = useQuery({
    queryKey: meQueryKey(),
    queryFn: () => api.me.current(),
  });

  const refresh = async () => {
    await queryClient.invalidateQueries({ queryKey: meQueryKey() });
    await queryClient.invalidateQueries({ queryKey: passkeysQueryKey() });
  };

  const accept = () => {
    void api.passkeys.add("This device").then(async () => {
      await api.me.dismissPasskeyOffer();
      await refresh();
    });
  };

  const dismiss = () => {
    void api.me.dismissPasskeyOffer().then(async () => {
      await queryClient.invalidateQueries({ queryKey: meQueryKey() });
    });
  };

  const data = me.data;
  if (!data || !showPasskeyOffer({ method: data.signIn, dismissed: data.offerDismissed })) {
    return null;
  }

  return (
    <div className="passkey-offer" role="region" aria-label="Add a passkey for next time">
      <Text>Add a passkey for next time</Text>
      <Button type="button" onClick={accept}>
        Add passkey
      </Button>
      <Button type="button" variant="default" onClick={dismiss}>
        Not now
      </Button>
    </div>
  );
}
