import "@mantine/core/styles.css";
import "@mantine/dates/styles.css";
import "@upmo-ai/tokens/tokens.css";
import "@upmo-ai/components/styles.css";
import "@fontsource/manrope/400.css";
import "@fontsource/manrope/500.css";
import "@fontsource/manrope/600.css";
import "@fontsource/manrope/700.css";
import "@fontsource/familjen-grotesk/700.css";
import "@fontsource/fira-code/400.css";

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { App } from "./App";
import { AppProviders } from "./theme";

const root = document.getElementById("root");
if (!root) throw new Error("root element missing");

createRoot(root).render(
  <StrictMode>
    <AppProviders>
      <App />
    </AppProviders>
  </StrictMode>,
);
