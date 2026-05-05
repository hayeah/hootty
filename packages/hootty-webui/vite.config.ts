import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite-plus";

// When running without devport (e.g. while devport itself is broken),
// set PTYDEMO_API=http://127.0.0.1:<port> before `vp dev` to proxy
// /api/* → that upstream and upgrade WS connections transparently.
// Devport normally handles this at its own proxy layer.
const ptydemoApi = process.env.PTYDEMO_API;

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": "/src",
    },
  },
  server: ptydemoApi
    ? {
        proxy: {
          "/api": {
            target: ptydemoApi,
            changeOrigin: true,
            ws: true,
            // ptydemo serve registers its handlers at /sessions,
            // /healthz, etc. — devport normally strips /api before
            // proxying. Match that behavior here.
            rewrite: (path) => path.replace(/^\/api/, ""),
          },
        },
      }
    : undefined,
  staged: {
    "*": "vp check --fix",
  },
  lint: {
    options: { typeAware: true, typeCheck: true },
  },
});
