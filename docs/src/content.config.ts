import { defineCollection } from "astro:content";
import { glob } from "astro/loaders";

// The docs are the plain markdown files next to this site, organised with
// Diátaxis. They have no frontmatter: the title is their first heading.
const docs = defineCollection({
  loader: glob({
    base: ".",
    pattern: [
      "README.md",
      "{tutorials,how-to,reference,explanation}/**/*.md",
    ],
    generateId: ({ entry }) => {
      const parts = entry.replace(/\.md$/, "").split("/");
      if (parts.at(-1) === "README") parts.pop();
      return parts.length ? parts.join("/") : "index";
    },
  }),
});

export const collections = { docs };
