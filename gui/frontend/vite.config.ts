import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { verityBridge } from "./vite.bridge";

// Wails serves the built assets from dist/ and injects the runtime + generated
// bindings under /wailsjs at build/dev time. verityBridge serves the same
// bindings over HTTP so `npm run dev` also works in a plain browser.
export default defineConfig({
  plugins: [react(), verityBridge()],
  build: {
    outDir: "dist",
  },
});
