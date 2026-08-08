import { useComputedColorScheme } from "@mantine/core";

import wordmarkDark from "./wordmark-dark.svg";
import wordmarkLight from "./wordmark-light.svg";

import "./wordmark.css";

export function Wordmark({ className }: { className?: string }) {
  const scheme = useComputedColorScheme("light", { getInitialValueInEffect: false });
  return (
    <img className={className} src={scheme === "dark" ? wordmarkDark : wordmarkLight} alt="Upmo" />
  );
}
