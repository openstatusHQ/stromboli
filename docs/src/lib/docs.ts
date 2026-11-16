import { getCollection, type CollectionEntry } from "astro:content";

export type Doc = CollectionEntry<"docs">;

export const SECTIONS = [
  { id: "tutorials", label: "Tutorials", blurb: "Learn by doing, step by step, with a result at the end." },
  { id: "how-to", label: "How-to guides", blurb: "Get a specific job done: connect Axiom, set up Slack…" },
  { id: "reference", label: "Reference", blurb: "Look up a CLI flag, a variable, a catalog field, an API." },
  { id: "explanation", label: "Explanation", blurb: "Understand how stromboli works and why." },
  { id: "adr", label: "Decisions", blurb: "The architecture decision records behind the design." },
] as const;

export function href(id: string): string {
  return id === "index" ? "/docs/" : `/docs/${id}/`;
}

/** The page title is its first `# heading`, without inline markdown. */
export function titleOf(doc: Doc): string {
  const match = doc.body?.match(/^#\s+(.+)$/m);
  const raw = match ? match[1] : doc.id;
  return raw.replace(/`([^`]+)`/g, "$1").replace(/\*\*([^*]+)\*\*/g, "$1").trim();
}

/** A shorter label for the sidebar: "How to set up Slack" -> "Set up Slack". */
export function navLabel(title: string): string {
  const short = title.replace(/^How to /i, "").replace(/ reference$/i, "");
  return short.charAt(0).toUpperCase() + short.slice(1);
}

/** The first paragraph after the title, as plain text, for meta descriptions. */
export function descriptionOf(doc: Doc): string {
  const body = (doc.body ?? "").replace(/^#\s+.+$/m, "");
  const para = body.split(/\n\s*\n/).map((p) => p.trim()).find((p) => p && !/^[#|>\-`!]/.test(p)) ?? "";
  return para
    .replace(/\[([^\]]+)\]\([^)]+\)/g, "$1")
    .replace(/[`*_]/g, "")
    .replace(/\s+/g, " ")
    .slice(0, 200);
}

export type NavItem = { id: string; title: string; href: string };
export type NavGroup = { label?: string; items: NavItem[] };
export type NavSection = { id: string; label: string; href: string; groups: NavGroup[] };

/**
 * Builds the sidebar. Each section keeps the order (and the `##` groups) of
 * its README, so the markdown stays the single source of truth; pages the
 * README does not list are appended in file order.
 */
export async function getNav(): Promise<{ sections: NavSection[]; flat: NavItem[] }> {
  const docs = await getCollection("docs");
  const byId = new Map(docs.map((d) => [d.id, d]));
  const item = (d: Doc): NavItem => ({ id: d.id, title: navLabel(titleOf(d)), href: href(d.id) });

  const sections = SECTIONS.map(({ id, label }) => {
    const index = byId.get(id);
    const pages = docs.filter((d) => d.id.startsWith(`${id}/`)).sort((a, b) => a.id.localeCompare(b.id));
    const groups: NavGroup[] = [];
    const seen = new Set<string>();
    let current: NavGroup = { items: [] };
    groups.push(current);

    for (const line of (index?.body ?? "").split("\n")) {
      const heading = line.match(/^##\s+(.+)$/);
      if (heading) {
        current = { label: heading[1].trim(), items: [] };
        groups.push(current);
        continue;
      }
      for (const [, target] of line.matchAll(/\]\(([\w.-]+)\.md(?:#[^)]*)?\)/g)) {
        const pageId = `${id}/${target}`;
        const page = byId.get(pageId);
        if (page && !seen.has(pageId)) {
          seen.add(pageId);
          current.items.push(item(page));
        }
      }
    }
    const rest = pages.filter((p) => !seen.has(p.id)).map(item);
    if (rest.length) groups.push({ label: groups.length > 1 ? "More" : undefined, items: rest });

    return { id, label, href: href(id), groups: groups.filter((g) => g.items.length) };
  });

  const flat: NavItem[] = [];
  const home = byId.get("index");
  if (home) flat.push({ ...item(home), title: "Overview" });
  for (const s of sections) {
    flat.push({ id: s.id, title: s.label, href: s.href });
    for (const g of s.groups) flat.push(...g.items);
  }
  return { sections, flat };
}
