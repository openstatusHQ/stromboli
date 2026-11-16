// Rewrites the relative links of the docs markdown so the same files work on
// GitHub and on the site:
//   ../how-to/self-host.md#tls  ->  /docs/how-to/self-host/#tls
//   reference/README.md         ->  /docs/reference/
//   ../../internal/agent        ->  https://github.com/openstatushq/stromboli/tree/main/internal/agent
import { existsSync, statSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const DOCS_ROOT = fileURLToPath(new URL("../..", import.meta.url));
const REPO_ROOT = path.dirname(DOCS_ROOT.replace(/\/$/, ""));
const REPO_URL = "https://github.com/openstatushq/stromboli";
// Markdown in docs/ that is not part of the site (see content.config.ts): the
// ADRs stay on GitHub.
const notPage = (rel) => rel === "adr" || rel.startsWith("adr/");

/** Maps a docs markdown path relative to docs/ to its site route. */
export function routeFor(rel) {
  const parts = rel.replace(/\.md$/, "").split("/");
  if (parts.at(-1) === "README") parts.pop();
  return parts.length ? `/docs/${parts.join("/")}/` : "/docs/";
}

function rewrite(href, fromFile) {
  if (!href || /^[a-z][a-z0-9+.-]*:/i.test(href) || href.startsWith("#") || href.startsWith("/")) {
    return href;
  }
  const [target, hash] = href.split("#");
  const abs = path.resolve(path.dirname(fromFile), target);
  const suffix = hash ? `#${hash}` : "";
  const relToDocs = path.relative(DOCS_ROOT, abs);
  const insideDocs = !relToDocs.startsWith("..") && !path.isAbsolute(relToDocs);

  if (insideDocs && abs.endsWith(".md") && !notPage(relToDocs)) return routeFor(relToDocs) + suffix;
  if (insideDocs && !notPage(relToDocs) && existsSync(path.join(abs, "README.md"))) {
    return routeFor(path.join(relToDocs, "README.md")) + suffix;
  }
  // Anything else (source files, folders, LICENSE...) lives on GitHub.
  const isDir = existsSync(abs) && statSync(abs).isDirectory();
  return `${REPO_URL}/${isDir ? "tree" : "blob"}/main/${path.relative(REPO_ROOT, abs)}${suffix}`;
}

function walk(node, fn) {
  fn(node);
  if (node.children) for (const child of node.children) walk(child, fn);
}

export function remarkDocLinks() {
  return (tree, file) => {
    if (!file.path) return;
    walk(tree, (node) => {
      if (node.type === "link" || node.type === "definition") {
        node.url = rewrite(node.url, file.path);
      }
    });
  };
}
