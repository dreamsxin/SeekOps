import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";

export default defineConfig({
  base: "/console/",
  plugins: [react()],
  build: {
    outDir: path.resolve(import.meta.dirname, "../internal/proxy/web"),
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      "/admin": "http://localhost:8080",
      "/healthz": "http://localhost:8080",
    },
  },
});
