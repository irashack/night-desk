# Night Desk roadmap

Night Desk is a complete daily-paper service that stands on its own and works with
any agent, or none. What exists today: gathering and source checks in an isolated
worker, drafting with OpenAI-compatible models or Claude, the draft and review
protocol over HTTP, MCP and signed webhooks, edition schemas v1 and v2, coverage
lean, web push, and an installable, offline-capable reader with a reading view
for stories the worker kept the text of. How it is built is in
[design.md](design.md).

Planned work keeps the existing boundaries: model calls and web fetching stay in
the worker, fetched text stays data, facts come from code, and the edition contract
and stored data keep the [upgrade promise](upgrading.md).

## Next

- **Fetch on first open:** when the gathering worker kept no snapshot of a story,
  the reading view offers only the original link. Whether the server should instead
  fetch the page on first open, through the guarded client that never follows
  private addresses, is an open product decision: it would be new outbound traffic
  from the server.

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
