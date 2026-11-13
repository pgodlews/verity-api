import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { verityBridge } from "./vite.bridge";

// Wails serves the built assets from dist/ and injects the runtime + generated
// bindings under /wailsjs at build/dev time. verityBridge serves the same
// bindings over HTTP so `npm run dev` also works in a plain browser.
export default defineConfig({
  plugins: [react(), verityBridge()],
  // The dev bridge runs local commands; never listen beyond this machine.
  server: { host: "localhost" },
  build: {
    outDir: "dist",
    // Retain the previous webview/browser target across the Vite upgrade.
    target: ["es2020", "edge88", "firefox78", "chrome87", "safari14"],
  },
});
