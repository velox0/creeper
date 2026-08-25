# Creeper

Creeper is a deterministic website crawler for scheduled sitemap generation and page discovery. It is built to run unattended from cron, a deployment pipeline, or a CI job.

It crawls same-host HTML links, generates a standards-compliant `sitemap.xml`, and can emit a JSON discovery artifact containing titles, status codes, crawl depth, link counts, and crawl errors. Failed responses, assets, external URLs, fragments, and—by default—query-string variants are excluded from the sitemap.

## Install

```sh
go install github.com/velox0/creeper@latest
```

Or build the checked-out source:

```sh
go build -trimpath -o creeper .
```

## Usage

Generating a sitemap is the default behavior:

```sh
creeper https://example.com
creeper -pages 5000 -depth 8 -output public/sitemap.xml https://example.com
creeper -quiet -fail-on-broken -discover-output artifacts/pages.json https://example.com
```

Useful automation flags:

| Flag | Default | Purpose |
|---|---:|---|
| `-o`, `-output` | `sitemap.xml` | Atomic sitemap destination; use `-` for stdout |
| `-discover-output` | off | Atomic JSON page-discovery report |
| `-n`, `-pages` | `1000` | Maximum successful HTML pages |
| `-d`, `-depth` | `0` | Maximum link depth; `0` is unlimited |
| `-timeout` | `15s` | Per-request HTTP deadline |
| `-run-timeout` | `10m` | Deadline for the complete crawl; `0` disables it |
| `-delay` | `0` | Politeness delay between requests, such as `200ms` |
| `-max-body` | `10485760` | Maximum bytes accepted per HTML response |
| `-fail-on-broken` | off | Exit `2` when any discovered page request fails |
| `-include-query` | off | Treat query strings as distinct pages |
| `-user-agent` | `creeper/...` | Override the HTTP User-Agent |
| `-quiet` | off | Suppress human-readable stderr output |
| `-track` | off | Persist content hashes and factor changes into priorities |
| `-history` | per-host file | Explicit change-history file, useful for a CI cache |
| `-port` | off | Fetch through `localhost:<port>` using the production Host header |

Durations use Go syntax: `500ms`, `30s`, `5m`.

## Exit codes

- `0`: crawl and requested outputs completed
- `1`: network, timeout, cancellation, history, or output failure
- `2`: invalid CLI usage, or broken pages with `-fail-on-broken`

Progress and warnings go to stderr, so `-output -` always keeps stdout valid XML.

## Cron

Run daily and replace the live sitemap only after a successful crawl:

```cron
15 2 * * * /usr/local/bin/creeper -quiet -run-timeout 20m -output /srv/www/sitemap.xml https://example.com >>/var/log/creeper.log 2>&1
```

Creeper writes files in the destination directory and atomically renames them into place. A failed run therefore leaves the previous sitemap intact.

## CI/CD

Example pipeline step:

```sh
./creeper \
  -quiet \
  -fail-on-broken \
  -pages 10000 \
  -run-timeout 15m \
  -output public/sitemap.xml \
  -discover-output artifacts/pages.json \
  https://example.com
```

To crawl a preview server while keeping production URLs in the generated sitemap:

```sh
creeper -port 8080 -output public/sitemap.xml https://example.com
```

Requests are sent to `http://localhost:8080` with `Host: example.com`; sitemap locations remain `https://example.com/...`.

## Change tracking

`-track` records content hashes across runs and increases the computed priority of pages that change frequently or recently. In ephemeral CI workers, point `-history` at a restored cache path:

```sh
creeper -track -history .cache/creeper/example.com.json https://example.com
```

The history contains timestamps and SHA-256 hashes, not page bodies.
