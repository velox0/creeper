# Creeper Web Crawler

A simple Go-based web crawler that visits pages on a domain, prints a summary of incoming links, and can generate a sitemap XML.

## Usage

```
go run main.go [flags] <url>
```

### Flags
- `-pages`, `-n <num>`     : Maximum number of pages to visit (default: 100)
- `-depth`, `-d <num>`     : Maximum recursion depth (0 = unlimited)
- `-summary`, `-s`         : Show summary table (default: true)
- `-xml`, `-x`             : Generate sitemap XML
- `-output`, `-o <file>`   : Output file for XML sitemap (default: `sitemap.xml`)
- `-local`, `-l`           : Crawl using localhost for the given domain (for local server testing; requests go to
localhost, but Host header is set to the original domain)
- `-port`, `-p <port>`     : Port to use (default: 80)
- `-track`, `-t`           : Track page content changes across runs to adjust sitemap priority

### Localhost Crawling

If you want to crawl a site served locally (e.g., for testing a production domain on your dev server), use the `-local`
flag. This will make all requests to `http://localhost` (or the port you specify with `-port`), but set the `Host`
header to the original domain. This is useful for generating xml for live website, but using your local server.

- When using `-local`, the crawler always uses the `http` protocol for requests to localhost, regardless of the original
URL's scheme.

#### Example

```
go run main.go -local -port 8080 -pages 20 example.com
```

This will crawl up to 20 pages, making requests to `http://localhost:8080`, but with the `Host` header set to
`example.com`. 