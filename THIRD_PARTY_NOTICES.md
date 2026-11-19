# Third-party notices

Night Desk's own code is MIT licensed; see [LICENSE](LICENSE). Dependencies keep
their upstream licences. Versions below are those declared in `go.mod` at this
phase's baseline; the v0.4.0 additions are identified separately.

## MuckScraper

Ported from [MuckScraper](https://github.com/grregis/MuckScraper), commit
`6c8ffaf`, Copyright (c) 2026 grregis, under the MIT License:

| Night Desk package | MuckScraper source |
| --- | --- |
| `internal/wire` | `news_fetcher/copy_detector.py` |
| `internal/byline` | `news_fetcher/bylines.py` |
| `internal/gnews` | `news_fetcher/google_news.py` |
| `internal/sourcecheck` | `news_fetcher/quality_checks.py` |

These v0.4.0 ports retain source attribution in their Go files and carry over
upstream test cases where available. MuckScraper also inspired keeping
opinion separate, withholding repeats, and using fast and writing model tiers;
v0.5.0 adds ingestion filters for junk such as betting picks and fantasy advice,
after its ingestion heuristics, and status-aware retries, host cool-offs and
extraction fallbacks after its scraper, all written afresh rather than ported.
Its Python runtime and application are not bundled.

The following is the complete text of MuckScraper's `LICENSE` at that commit:

```text
MIT License

Copyright (c) 2026 grregis

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## Go modules

Direct dependencies currently in `go.mod`:

| Module | Version | Licence |
| --- | --- | --- |
| `github.com/anthropics/anthropic-sdk-go` | v1.84.0 | MIT |
| `github.com/coreos/go-oidc/v3` | v3.21.0 | Apache-2.0 |
| `golang.org/x/image` | v0.47.0 | BSD-3-Clause |
| `golang.org/x/net` | v0.62.0 | BSD-3-Clause |
| `golang.org/x/oauth2` | v0.37.0 | BSD-3-Clause |
| `modernc.org/sqlite` | v1.61.0 | BSD-3-Clause (SQLite itself is public domain) |
| `sigs.k8s.io/yaml` | v1.6.0 | MIT + BSD-3-Clause; underlying YAML parser licences below |

Indirect dependencies currently in `go.mod`:

| Module | Version | Licence |
| --- | --- | --- |
| `github.com/bahlo/generic-list-go` | v0.2.0 | BSD-3-Clause |
| `github.com/buger/jsonparser` | v1.1.2 | MIT |
| `github.com/dustin/go-humanize` | v1.1.0 | MIT |
| `github.com/go-jose/go-jose/v4` | v4.1.4 | Apache-2.0; bundled Go JSON code BSD-3-Clause |
| `github.com/google/uuid` | v1.6.0 | BSD-3-Clause |
| `github.com/invopop/jsonschema` | v0.14.0 | MIT |
| `github.com/mattn/go-isatty` | v0.0.24 | MIT |
| `github.com/ncruces/go-strftime` | v1.1.0 | MIT |
| `github.com/pb33f/ordered-map/v2` | v2.3.1 | Apache-2.0 |
| `github.com/remyoudompheng/bigfft` | v0.0.0-20230129092748-24d4a6f8daec | BSD-3-Clause |
| `github.com/standard-webhooks/standard-webhooks/libraries` | v0.0.1 | MIT |
| `github.com/tidwall/gjson`, `match`, `pretty`, `sjson` | v1.18.0, v1.1.1, v1.2.1, v1.2.5 | MIT |
| `go.yaml.in/yaml/v2` | v2.4.2 | Apache-2.0; libyaml-derived files MIT |
| `go.yaml.in/yaml/v4` | v4.0.0-rc.2 | MIT |
| `golang.org/x/sys` | v0.49.0 | BSD-3-Clause |
| `modernc.org/libc` | v1.79.0 | BSD-3-Clause; translated libc sources retain upstream notices |
| `modernc.org/mathutil` | v1.7.1 | BSD-3-Clause |
| `modernc.org/memory` | v1.12.1 | BSD-3-Clause |

Additional modules named by the v0.4.0 roadmap, added with the implementing
phases (not yet in this baseline's `go.mod`):

| Module | Roadmap version | Licence |
| --- | --- | --- |
| `github.com/mmcdole/gofeed` | v1.5.0 | MIT |
| `codeberg.org/readeck/go-readability/v2` | v2.1.3 | MIT |
| `github.com/modelcontextprotocol/go-sdk` | v1.8.0 | MIT |
| `go.opentelemetry.io/otel` and its SDK, trace and OTLP exporter modules | v1.47.0 | Apache-2.0 |
| Additional `golang.org/x/*` modules | As selected in `go.mod` | BSD-3-Clause |

Consult each selected module's `LICENSE`, `NOTICE` and source headers for full
terms and embedded-code notices; Go downloads retain them in the module cache.
The actual build's dependency versions are recorded in `go.mod`/`go.sum` and
binary build metadata.

## Fonts

Old Standard TT, Libre Franklin and UnifrakturCook are bundled under the SIL Open
Font License 1.1. Their full copyright and licence texts accompany the fonts in
[`web/static/fonts/`](web/static/fonts/).

## Ideas and optional operator-imported data

[Ground News](https://ground.news/) inspired the coverage bar and blindspot
presentation. [AllSides](https://www.allsides.com/media-bias/media-bias-ratings)
informed the outlet-ratings idea. No Ground News or AllSides ratings or datasets
are bundled. Operators may import their own ratings only under the data
provider's terms and must preserve source and attribution. These credits do not
grant rights to third-party datasets or imply endorsement.

[OpenTelemetry](https://opentelemetry.io/) provides the tracing conventions;
[Langfuse](https://langfuse.com/) is an optional operator-selected OTLP receiver,
not a bundled service.
