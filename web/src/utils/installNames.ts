// Names a marketplace install gives the resources it creates.
//
// Every resource is named after the *install*, not the template, so the same
// template can be installed twice without the second install colliding with the
// first. The server is authoritative (internal/services/marketplace:
// resourceName + createApp, internal/slug.Make); this mirrors the rule so the
// install form can show what a name will actually be before anything is created.
// The two are covered by matching test cases — a preview that lies is worse than
// no preview at all.
//
// One thing this cannot mirror: a name already taken in the workspace gets a
// numeric suffix from the server, which the browser has no way to know.

// slugify matches internal/slug.Make: lowercase, non-alphanumerics collapsed to
// a hyphen, hyphens trimmed off both ends, falling back when nothing is left.
export function slugify(name: string, fallback = ''): string {
  const s = name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/(^-+|-+$)/g, '')
  return s || fallback
}

// resourceName is the workspace name of a volume or config: the install label
// joined to the template-local name ("Smart" + "content" → "smart-content").
export function resourceName(installLabel: string, localName: string): string {
  return slugify(`${installLabel}-${localName}`, localName)
}

// displayFor is the label an app, database or volume is created with. A single
// app or database is named for the install alone; several are suffixed with the
// template-local name so they stay apart. Volumes always carry theirs — a
// "content" and a "db" volume are not interchangeable.
export function displayFor(installLabel: string, localName: string, count: number): string {
  return count > 1 ? `${installLabel} ${localName}` : installLabel
}
