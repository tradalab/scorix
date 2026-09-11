import { fileURLToPath, URL } from "node:url"
import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { defineConfig } from "vite"

export default defineConfig({
  plugins: [react(), tailwindcss()],
  // Relative: the shell is served from scorix://app/, not from a host root.
  base: "./",
  build: { outDir: "dist" },
  server: {
    // scorix dev sets PORT and waits on it, and Vite ignores PORT on its own.
    // strictPort: fail loudly rather than drift to a port nobody watches.
    port: Number(process.env.PORT) || 5173,
    strictPort: true,
  },
  resolve: {
    // "@" is the shell root, not src/: generate writes types/ and api/ there in every scaffold.
    alias: { "@": fileURLToPath(new URL(".", import.meta.url)) },
  },
})
