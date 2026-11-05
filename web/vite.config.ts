import { fileURLToPath, URL } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  server: {
    port: 5173,
    // The Go server's admin listener uses a self-signed certificate in development.
    proxy: {
      "/api": { target: "https://127.0.0.1:8443", secure: false, changeOrigin: true },
    },
  },
});
