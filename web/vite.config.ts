import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";
import { brandKit } from "./plugins/brand-kit";

// Vite's proxy server type extends node's EventEmitter, and without node's
// type declarations installed it arrives without `on`. These are the members
// the stream hooks actually touch.
type StreamProxy = {
  on(event: "proxyReq", listener: (proxyReq: { removeHeader(name: string): void }, req: { url?: string }) => void): void;
} & {
  on(event: "proxyRes", listener: (proxyRes: { headers: { [name: string]: string | string[] | undefined } }) => void): void;
};

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
          const events = proxy as unknown as StreamProxy;
          events.on("proxyReq", (proxyReq, req) => {
            if (req.url?.includes("/stream")) proxyReq.removeHeader("accept-encoding");
          });
          events.on("proxyRes", (proxyRes) => {
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
