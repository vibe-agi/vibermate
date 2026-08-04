import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [
    {
      name: "vibermate-development-style-csp",
      apply: "serve",
      transformIndexHtml(html) {
        // Vite injects transformed CSS through a style element while serving.
        // The packaged build emits an external stylesheet and keeps the
        // stricter index.html policy unchanged.
        return html.replace(
          "style-src 'self'",
          "style-src 'self' 'unsafe-inline'",
        );
      },
    },
    react(),
  ],
  server: {
    host: "127.0.0.1",
    port: 1420,
    strictPort: true,
  },
  test: {
    environment: "jsdom",
    include: ["test/**/*.test.{ts,tsx}"],
    restoreMocks: true,
    setupFiles: ["./test/setup.ts"],
  },
});
