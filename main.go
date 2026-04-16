package main

import (
	"flag"
	"fmt"
	"net/url"
	"strings"
)

func main() {
	maxPages := flag.Int("pages", 100, "Maximum number of pages to visit (shorthand -n)")
	flag.IntVar(maxPages, "n", 100, "")
	maxDepth := flag.Int("depth", 0, "Maximum recursion depth (0 = unlimited, shorthand -d)")
	flag.IntVar(maxDepth, "d", 0, "")
	showSummary := flag.Bool("summary", true, "Show summary of incoming links at the end (shorthand -s)")
	flag.BoolVar(showSummary, "s", true, "")
	xmlOut := flag.Bool("xml", false, "Generate XML sitemap (shorthand -x)")
	flag.BoolVar(xmlOut, "x", false, "")
	outFile := flag.String("output", "sitemap.xml", "Output file for XML sitemap (shorthand -o)")
	flag.StringVar(outFile, "o", "sitemap.xml", "")
	useLocalhost := flag.Bool("local", false, "Crawl using localhost for the given domain (shorthand -l)")
	flag.BoolVar(useLocalhost, "l", false, "")
	port := flag.Int("port", 80, "Port to use when crawling localhost (shorthand -p)")
	flag.IntVar(port, "p", 80, "")
	trackChanges := flag.Bool("track", false, "Track page content changes across runs; bumps sitemap priority for frequently/recently updated pages (shorthand -t)")
	flag.BoolVar(trackChanges, "t", false, "")
	flag.Parse()

	// Resolve start URL.
	var startURL string
	if flag.NArg() < 1 {
		startURL = fmt.Sprintf("http://localhost:%d", *port)
		*useLocalhost = false
	} else {
		startURL = flag.Arg(0)
		if *port != 80 && !*useLocalhost {
			*useLocalhost = true
		}
	}
	if !strings.HasPrefix(startURL, "http://") && !strings.HasPrefix(startURL, "https://") {
		if strings.HasPrefix(startURL, "localhost") {
			startURL = "http://" + startURL
		} else {
			startURL = "https://" + startURL
		}
	}

	base, err := url.Parse(startURL)
	if err != nil {
		fmt.Println("Invalid URL:", err)
		return
	}

	cfg := fetchConfig{
		useLocalhost: *useLocalhost,
		port:         *port,
		origHost:     base.Host,
	}

	// When localhost mode is active, rewrite the initial fetch URL.
	firstURL := base
	if cfg.useLocalhost {
		local := *base
		local.Scheme = "http"
		local.Host = "localhost"
		if cfg.port != 80 {
			local.Host = fmt.Sprintf("localhost:%d", cfg.port)
		}
		firstURL = &local
	}

	// Load change history when tracking is enabled.
	var history *ChangeHistory
	var histFile string
	if *trackChanges {
		histFile = historyFilename(cfg.origHost)
		history = loadHistory(histFile)
		if n := len(history.Pages); n > 0 {
			fmt.Printf("Loaded change history for %d page(s) from %s\n", n, histFile)
		}
	}

	body, doc, err := fetch(firstURL, cfg)
	if err != nil {
		fmt.Println("Error fetching URL:", err)
		return
	}

	normStart := normalizeURL(base)
	startPath := pathOnly(base)

	if history != nil {
		history.record(normStart, body)
	}

	state := newCrawlerState(normStart, startPath, *maxPages, *maxDepth)
	visitLinks(base, doc, state, normStart, 1, cfg, history)

	// Persist updated history.
	if history != nil {
		if err := saveHistory(history, histFile); err != nil {
			fmt.Printf("Warning: could not save change history to %s: %v\n", histFile, err)
		} else {
			fmt.Printf("Change history saved to %s (%d page(s))\n", histFile, len(history.Pages))
		}
	}

	if *showSummary {
		fmt.Println("\nSummary of visited pages and incoming link counts:")
		printSummaryTable(state, startPath)
	}
	if *xmlOut {
		if err := writeXML(state, *outFile, history); err != nil {
			fmt.Println("Error writing XML:", err)
		} else {
			fmt.Printf("XML written to %s\n", *outFile)
		}
	}
}
