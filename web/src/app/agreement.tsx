import { Anchor, Text } from "@mantine/core";
import { Link } from "@tanstack/react-router";

export function LoginAgreement() {
  return (
    <Text className="sign-in-agreement" size="sm">
      By logging in, you agree to the <Anchor component={Link} to="/terms">Terms of use</Anchor> and the{" "}
      <Anchor component={Link} to="/privacy">Privacy policy</Anchor>.
    </Text>
  );
}
