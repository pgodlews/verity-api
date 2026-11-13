import { writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import { verityBridge } from "./vite.bridge";

// dist/ is committed with only .gitkeep, so `go build` (which embeds
// frontend/dist) works before the first frontend build. The build empties
// dist/; put the placeholder back so a build leaves the tree clean.
function keepDistPlaceholder(): Plugin {
  return {
    name: "verity-keep-dist-placeholder",
    apply: "build",
    writeBundle(options) {
      writeFileSync(resolve(options.dir ?? resolve(__dirname, "dist"), ".gitkeep"), "");
    },
  };
}

// Wails serves the built assets from dist/ and injects the runtime + generated
// bindings under /wailsjs at build/dev time. verityBridge serves the same
// bindings over HTTP so `npm run dev` also works in a plain browser.
export default defineConfig({
  plugins: [react(), verityBridge(), keepDistPlaceholder()],
  // The dev bridge runs local commands; never listen beyond this machine.
  server: { host: "localhost" },
  build: {
    outDir: "dist",
    // Retain the previous webview/browser target across the Vite upgrade.
    target: ["es2020", "edge88", "firefox78", "chrome87", "safari14"],
  },
});
