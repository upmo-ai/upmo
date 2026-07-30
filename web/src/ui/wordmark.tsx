import { useComputedColorScheme } from "@mantine/core";

import wordmarkDark from "./wordmark-dark.svg";
import wordmarkLight from "./wordmark-light.svg";

export function Wordmark({ className = "shell-wordmark" }: { className?: string }) {
  const scheme = useComputedColorScheme("light", { getInitialValueInEffect: false });
  return (
    <img className={className} src={scheme === "dark" ? wordmarkDark : wordmarkLight} alt="Upmo" />
  );
}
