// @ts-check
import { defineConfig } from "astro/config";
import { remarkDocLinks } from "./src/lib/remark-doc-links.mjs";

export default defineConfig({
  site: "https://stromboli.dev",
  // The markdown in this folder is the source of truth, and it is also read on
  // GitHub. Its relative links (`../how-to/self-host.md`) are rewritten to site
  // routes at build time.
  markdown: {
    remarkPlugins: [remarkDocLinks],
    shikiConfig: {
      themes: { light: "github-light", dark: "github-dark" },
      defaultColor: false,
    },
  },
});
