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

- A reader with several papers of their own (group papers already give a reader
  several shared papers, with a switcher).
- More looks, each a stylesheet over shared tokens.
- NewsAPI and GNews as optional sources, if feeds prove thin.
- A default deadline policy other than `hold`, if experience with unattended
  papers shows `publish-unreviewed` is the better default.
- The section editor's history: the paper already keeps 50 numbered editorial
  revisions with restore, and sections have `required`, `find_sources` and `lean`
  flags, but `/sections` shows neither; an undo or recent-changes panel and the
  flags as toggles.
- Archive search by byline, a column's stance and further-coverage outlets
  (schema v2 fields the full-text index does not cover yet).
- One gathering worker for several papers: one process looping over several
  worker keys, sharing its models and host cool-off, instead of one container
  per paper.
- The markets box in a section of the operator's choice; today it shows only in a
  section keyed `economy`, which standalone papers do not have.
- `nightdesk accounts role` (make an account an administrator or a reader without
  the web page) and `nightdesk key rotate` (issue a matching key, revoke the old).

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
