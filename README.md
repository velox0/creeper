# Creeper Web Crawler

A simple Go-based web crawler that visits pages on a domain, prints a summary of incoming links, and can generate a sitemap XML.

## Usage

```
go run main.go [flags] <url>
```

### Flags
- `-pages`, `-n <num>`     : Maximum number of pages to visit (default: 100)
- `-depth`, `-d <num>`     : Maximum recursion depth (0 = unlimited)
- `-shh`, `-s`             : Turn off summary of incoming links
- `-verbose`, `-v`         : Show weights of pages in the summary table
- `-xml`, `-x`             : Generate sitemap XML
- `-output`, `-o <file>`   : Output file for XML sitemap (default: `sitemap.xml`)
- `-port`, `-p <port>`     : Port to use (default: 80)
- `-track`, `-t`           : Track page content changes across runs to adjust sitemap priority

### Localhost Crawling

If you want to crawl a site served locally (e.g., for testing a production domain on your dev server), simply provide a custom `-port`. The crawler will automatically make all requests to `http://localhost` on that port, but will set the `Host` header to the original domain. This is useful for generating XML for a live website using your local server.

- When crawling locally, the crawler always uses the `http` protocol for requests to localhost, regardless of the original URL's scheme.

#### Example

```
go run main.go -port 8080 -pages 20 example.com
```

This will crawl up to 20 pages, making requests to `http://localhost:8080`, but with the `Host` header set to
`example.com`. 