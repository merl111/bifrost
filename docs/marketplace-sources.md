# Public skill catalog APIs

Status: required by the user on 2026-10-15; documented for the next developer.
This document does not implement API adapters or enable a remote source.

## Current behavior

`internal/marketplace/catalog.go` merges three bundled, pinned skills with one
optional HTTPS JSON catalog configured by `marketplace.catalog_url`. That custom
catalog must use Bifrost's `entries` schema: ID, name, description, repository,
ref and path. The client loads the catalog; the TUI filters the loaded entries
locally. There is no remote paginated search or source federation yet.

The existing grid/detail modal and both install paths must remain usable:
`:skill install ID [--scope project|global]` and
`bifrost skill market install ID --scope project|global`.

## Verified public API candidates

Documentation reviewed 2026-10-15. No authenticated/live API conformance tests
were run. Recheck contracts before implementing; public documentation does not
mean anonymous, unlimited, or permanently free service.

| Source | Documented interface | Access considerations |
| --- | --- | --- |
| SkillsMP | `GET https://skillsmp.com/api/v1/skills/search`, query `q`, page/limit and optional filters | Documents limited anonymous keyword search and optional bearer API keys. Respect quota and Retry-After; wildcard queries are unsupported. |
| skills.sh | `https://skills.sh/api/v1/skills`, `/skills/search`, `/skills/curated`, and skill detail/audit routes | Documents Vercel OIDC authentication. Do not assume a desktop app can obtain a token anonymously. Treat as optional until the supported desktop credential route is verified. |

Sources: [SkillsMP API documentation](https://skillsmp.com/docs/api),
[skills.sh API reference](https://www.skills.sh/docs/api).

Recommended first adapter: SkillsMP keyword search because its documented
anonymous mode fits a standalone CLI. Keep built-in/custom sources working
offline. Add other documented APIs through the same boundary; do not implement
HTML scraping or depend on a third-party Node CLI to discover/install skills.

The skills.sh search/list objects expose catalog identity and source information;
details can include files/content hashes. Do not assume a catalog ID is a Git
subdirectory or a content hash is a Git commit SHA. Resolve install provenance
explicitly before passing an item to the existing installer.

## Required integration behavior

- Use small Go adapters with the standard `net/http` and `encoding/json`
  packages. Separate source search/detail resolution from installation. Keep
  source-specific schemas out of Bubble Tea view code.
- Normalize source identity, upstream ID, name, description, canonical link,
  repository/path/ref when available, and installation capability. Use a stable
  source-qualified identity so two catalogs cannot overwrite one another's IDs.
  Deduplicate equivalent install targets without losing source attribution.
- Preserve existing bundled IDs and commands. Current `idPattern` only accepts
  lowercase alphanumeric IDs with `-_.` separators; upstream IDs may contain
  slashes. Design explicit ID mapping/parsing and round-trip tests rather than
  passing upstream IDs blindly into `Client.Get` or loosening path validation.
- Show results in the existing cards with short descriptions and a compact
  source label. Open source details in the existing modal. Populate missing
  descriptions from bounded detail/metadata retrieval where supported; do not
  make one blocking request per card during rendering or fabricate descriptions.
- Keep local filtering immediate. Remote search should be explicit or suitably
  debounced and cancellation-aware, with paging, stale-result guards, visible
  loading and source-specific errors. Prefer explicit submission for small daily
  quotas. Never spend a quota request on every keystroke by default.
- Cache bounded results and respect source cache headers. Retain useful results
  on transient failures; show stale/offline state. One unavailable source must
  not make all other sources unusable. Handle 400/401/403/429/5xx distinctly and
  honor Retry-After without a busy retry loop.
- Never send conversation history, source code or private project data as a
  search query implicitly. Send only the user's intended skill search terms.
- Configure enabled sources, endpoint and credential environment references
  through the existing config/editor. Keep `marketplace.catalog_url` compatible.
  Schema/defaults/migration details are part of implementation, not added by this
  document. No credential values in settings or query URLs; no cross-origin
  authorization-header forwarding on redirects.
- Validate response sizes and metadata. Resolve a reproducible commit and actual
  skill directory before Git installation; unsupported source formats should be
  labeled un-installable until a validated install route exists. Retain safe
  archive/path handling and the exact previewed install target.
- Discovery, download/install and activation are separate. API metadata is data,
  never instructions to execute. Do not run upstream install scripts or `npx`
  commands from catalog descriptions. Popularity/audit badges are attributed
  metadata, not a guarantee that code is trusted.
- TUI and CLI must use the same source registry/resolver. Extend command help,
  completion and source disambiguation together. An explicit install command
  must resolve its target even if the marketplace has never been opened.

## Code locations and acceptance

Start with `internal/marketplace/catalog.go`, `internal/cli/marketplace.go`,
`internal/cli/ui_options.go`, `internal/config/settings.go`, and the marketplace/
skill-command files in `internal/ui`. `MarketplaceCallbacks.List` currently
returns a complete slice; evolving it for paging must preserve cancellation and
request IDs. Reuse `internal/skill` for installation.

1. Implement a source contract and one documented API adapter with loopback HTTP
   fixtures. Cover real recorded response shapes, malformed/oversized results,
   missing metadata, pagination, auth failure, rate limits and cancellation.
2. Wire configuration and asynchronous search into the existing card grid; test
   partial source failure, source collisions, query changes during requests and
   keyboard/mouse selection while results update. Preserve selection by identity.
3. Verify detail resolution and both install paths against disposable local Git
   fixtures. Cover slash-containing upstream IDs, changed remote refs, invalid
   paths, pinned revisions, project/global scope and no automatic activation.
4. Run optional read-only live searches within the provider's limits, separately
   from deterministic CI. Record date, endpoint and authentication mode; fixture
   success alone is not live compatibility evidence.
5. Update README/config examples/current-build and mark only tested sources as
   supported. Keep this requirement linked from AGENTS.md until complete.
