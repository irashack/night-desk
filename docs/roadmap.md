# Night Desk roadmap

Night Desk is a complete daily-paper service that stands on its own and works with
any agent, or none. What exists today: gathering and source checks in an isolated
worker, drafting with OpenAI-compatible models or Claude, the draft and review
protocol over HTTP, MCP and signed webhooks, edition schemas v1 and v2, coverage
lean, web push, and an installable, offline-capable reader. How it is built is in
[design.md](design.md).

Planned work keeps the existing boundaries: model calls and web fetching stay in
the worker, fetched text stays data, facts come from code, and the edition contract
and stored data keep the [upgrade promise](upgrading.md).

## Next: a reading view

- [ ] **Read here:** a story opens in Night Desk itself, set in the paper's own
  type, with a sticky "Back to the paper" bar that returns to the same page and
  place, and a prominent "Open at <outlet>" link to the original.
- [ ] **Where the text comes from:** the gathering worker's stored snapshot when
  there is one; otherwise either the server fetches the page on first open through
  the guarded client and keeps it with the edition, or the view offers only the
  original link. Which of the two is still open: a fetch would be new outbound
  traffic from the server, and it must never follow private addresses.
- [ ] **Honest limits:** a paywalled or script-only page shows what extraction
  gets and says so, with the original link first; the reading view is for the
  paper's own readers and is never public or shared.
- [ ] **Reading comforts:** text size and the reading layout's measure apply; the
  last-read position in a story is kept per device.

## Later

- A reader with several papers (the data model already allows it).
- More looks, each a stylesheet over shared tokens.
- NewsAPI and GNews as optional sources, if feeds prove thin.
- A default deadline policy other than `hold`, if experience with unattended
  papers shows `publish-unreviewed` is the better default.

## Credits

- [MuckScraper](https://github.com/grregis/MuckScraper) by grregis (MIT): wire-copy
  detection by text overlap, checking generated text against its sources,
  resolving site-limited Google News feeds to publisher URLs, keeping opinion in
  its own lane, fast and writing model tiers, bylines from JSON-LD, holding back
  repeats, ingestion filters and headline truncation rules. Ideas are credited
  here; ported code keeps MuckScraper's MIT notice (see
  [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md)).
- [Ground News](https://ground.news/): the coverage bar and blindspot ideas.
- [Langfuse](https://langfuse.com/) and [OpenTelemetry](https://opentelemetry.io/):
  tracing model calls; Langfuse is optional, through OTLP.
- [Meilisearch](https://www.meilisearch.com/) was considered for search and
  declined in favor of SQLite full-text search, to keep one binary and one
  database.

Night Desk ships no outlet ratings. Operators who enable coverage lean import
ratings from a source they are licensed to use;
[AllSides](https://www.allsides.com/media-bias/media-bias-ratings) is one example,
under its own terms.
