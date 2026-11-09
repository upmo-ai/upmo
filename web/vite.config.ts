import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";
import { brandKit } from "./plugins/brand-kit";

export default defineConfig({
  plugins: [react(), brandKit()],
  server: {
    host: "0.0.0.0",
    port: 5173,
    strictPort: true,
    watch: {
      usePolling: true,
      interval: 300,
    },
    proxy: {
      "/api": {
        target: (globalThis as { process?: { env?: { UPMO_API_PROXY?: string } } }).process?.env?.UPMO_API_PROXY ?? "http://brain:8080",
        changeOrigin: true,
        // The task event stream must arrive as it is written: never compressed,
        // never buffered. The stream request stops asking for compression, and
        // a text/event-stream answer is marked not bufferable before its
        // headers are copied to the browser.
        configure(proxy) {
          proxy.on("proxyReq", (proxyReq, req) => {
            if (req.url?.includes("/stream")) proxyReq.removeHeader("accept-encoding");
          });
          proxy.on("proxyRes", (proxyRes) => {
            const contentType = String(proxyRes.headers["content-type"] ?? "");
            if (contentType.startsWith("text/event-stream")) {
              proxyRes.headers["x-accel-buffering"] = "no";
              delete proxyRes.headers["content-encoding"];
            }
          });
        },
      },
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
  },
});
