# Third-party notices

Night Desk's own code is MIT licensed; see [LICENSE](LICENSE). Dependencies keep
their upstream licences.

## MuckScraper

Ported from [MuckScraper](https://github.com/grregis/MuckScraper), commit
`6c8ffaf`, Copyright (c) 2026 grregis, under the MIT License:

| Night Desk package | MuckScraper source |
| --- | --- |
| `internal/wire` | `news_fetcher/copy_detector.py` |
| `internal/byline` | `news_fetcher/bylines.py` |
| `internal/gnews` | `news_fetcher/google_news.py` |
| `internal/sourcecheck` | `news_fetcher/quality_checks.py` |

These ports keep source attribution in their Go files and carry over upstream
test cases where available. MuckScraper also inspired keeping opinion separate,
withholding repeats, using fast and writing model tiers, the ingestion filters
for junk such as betting picks and fantasy advice (after its ingestion
heuristics), and the status-aware retries, host cool-offs and extraction
fallbacks in the article fetcher (after its scraper); those are written afresh
rather than ported. Its Python runtime and application are not bundled.

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

These are the modules compiled into the `nightdesk` binary (from
`go list -deps ./cmd/...` for linux and darwin on amd64 and arm64), at the
versions selected in `go.mod`. Modules that appear in `go.mod` or `go.sum` only
for tests or tooling are not linked and are not listed.

Direct dependencies:

| Module | Version | Licence |
| --- | --- | --- |
| `codeberg.org/readeck/go-readability/v2` | v2.1.3 | MIT |
| `github.com/anthropics/anthropic-sdk-go` | v1.84.0 | MIT |
| `github.com/coreos/go-oidc/v3` | v3.21.0 | Apache-2.0 (with NOTICE) |
| `github.com/mmcdole/gofeed` | v1.5.0 | MIT |
| `github.com/modelcontextprotocol/go-sdk` | v1.8.0 | Apache-2.0; earlier contributions MIT |
| `go.opentelemetry.io/otel` | v1.47.0 | Apache-2.0 |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp` | v1.47.0 | Apache-2.0 |
| `go.opentelemetry.io/otel/sdk` | v1.47.0 | Apache-2.0 |
| `go.opentelemetry.io/otel/trace` | v1.47.0 | Apache-2.0 |
| `golang.org/x/image` | v0.48.0 | BSD-3-Clause |
| `golang.org/x/net` | v0.62.0 | BSD-3-Clause |
| `golang.org/x/oauth2` | v0.38.0 | BSD-3-Clause |
| `modernc.org/sqlite` | v1.62.0 | BSD-3-Clause; SQLite itself is public domain; see its `LICENSE-3RD-PARTY.md` |
| `sigs.k8s.io/yaml` | v1.6.0 | MIT and BSD-3-Clause; bundled YAML parser code Apache-2.0 and MIT |

Indirect dependencies:

| Module | Version | Licence |
| --- | --- | --- |
| `github.com/andybalholm/cascadia` | v1.3.3 | BSD-2-Clause |
| `github.com/bahlo/generic-list-go` | v0.2.0 | BSD-3-Clause |
| `github.com/buger/jsonparser` | v1.1.2 | MIT |
| `github.com/cenkalti/backoff/v5` | v5.0.3 | MIT |
| `github.com/cespare/xxhash/v2` | v2.3.0 | MIT |
| `github.com/dustin/go-humanize` | v1.1.0 | MIT |
| `github.com/go-jose/go-jose/v4` | v4.1.4 | Apache-2.0; bundled Go JSON code BSD-3-Clause |
| `github.com/go-logr/logr` | v1.4.4 | Apache-2.0 |
| `github.com/go-logr/stdr` | v1.2.2 | Apache-2.0 |
| `github.com/go-shiori/dom` | v0.0.0-20230515143342-73569d674e1c | MIT |
| `github.com/gogs/chardet` | v0.0.0-20211120154057-b7413eaefb8f | MIT |
| `github.com/google/jsonschema-go` | v0.4.3 | MIT |
| `github.com/google/uuid` | v1.6.0 | BSD-3-Clause |
| `github.com/grpc-ecosystem/grpc-gateway/v2` | v2.30.0 | BSD-3-Clause |
| `github.com/invopop/jsonschema` | v0.14.0 | MIT |
| `github.com/itlightning/dateparse` | v0.2.1 | MIT |
| `github.com/mattn/go-isatty` | v0.0.24 | MIT (macOS builds only) |
| `github.com/mmcdole/goxpp/v2` | v2.0.0 | MIT |
| `github.com/ncruces/go-strftime` | v1.1.0 | MIT (macOS builds only) |
| `github.com/pb33f/ordered-map/v2` | v2.3.1 | Apache-2.0 |
| `github.com/remyoudompheng/bigfft` | v0.0.0-20230129092748-24d4a6f8daec | BSD-3-Clause |
| `github.com/segmentio/asm` | v1.1.3 | MIT |
| `github.com/segmentio/encoding` | v0.5.4 | MIT |
| `github.com/standard-webhooks/standard-webhooks/libraries` | v0.0.1 | MIT |
| `github.com/tidwall/gjson` | v1.18.0 | MIT |
| `github.com/tidwall/match` | v1.1.1 | MIT |
| `github.com/tidwall/pretty` | v1.2.1 | MIT |
| `github.com/tidwall/sjson` | v1.2.5 | MIT |
| `github.com/yosida95/uritemplate/v3` | v3.0.2 | BSD-3-Clause |
| `go.opentelemetry.io/auto/sdk` | v1.2.1 | Apache-2.0 |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace` | v1.47.0 | Apache-2.0 |
| `go.opentelemetry.io/otel/log` | v1.47.0 | Apache-2.0 |
| `go.opentelemetry.io/otel/metric` | v1.47.0 | Apache-2.0 |
| `go.opentelemetry.io/proto/otlp` | v1.11.0 | Apache-2.0 |
| `go.yaml.in/yaml/v2` | v2.4.2 | Apache-2.0 (with NOTICE); libyaml-derived files MIT |
| `go.yaml.in/yaml/v4` | v4.0.0-rc.2 | Apache-2.0 (with NOTICE); libyaml-derived files MIT |
| `golang.org/x/sync` | v0.25.0 | BSD-3-Clause |
| `golang.org/x/sys` | v0.50.0 | BSD-3-Clause |
| `golang.org/x/text` | v0.44.0 | BSD-3-Clause |
| `golang.org/x/time` | v0.15.0 | BSD-3-Clause |
| `google.golang.org/genproto/googleapis/api` | v0.0.0-20260928230214-8a89bd6388cc | Apache-2.0 |
| `google.golang.org/genproto/googleapis/rpc` | v0.0.0-20260928230214-8a89bd6388cc | Apache-2.0 |
| `google.golang.org/grpc` | v1.83.2 | Apache-2.0 (with NOTICE) |
| `google.golang.org/protobuf` | v1.36.12 | BSD-3-Clause |
| `modernc.org/libc` | v1.80.1 | BSD-3-Clause; bundled Go and musl code keep their notices; see its `LICENSE-3RD-PARTY.md` |
| `modernc.org/mathutil` | v1.7.1 | BSD-3-Clause |
| `modernc.org/memory` | v1.12.1 | BSD-3-Clause; bundled Go code BSD-3-Clause |

Consult each module's `LICENSE`, `NOTICE` and source headers for full terms and
embedded-code notices; Go keeps them in the module cache. The versions in a
given build are recorded in `go.mod`, `go.sum` and the binary's build metadata
(`go version -m nightdesk`).

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
