package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("creeper", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pages := fs.Int("pages", 1000, "maximum successful HTML pages to crawl")
	fs.IntVar(pages, "n", 1000, "shorthand for -pages")
	depth := fs.Int("depth", 0, "maximum link depth (0 = unlimited)")
	fs.IntVar(depth, "d", 0, "shorthand for -depth")
	output := fs.String("output", "sitemap.xml", "sitemap output path ('-' for stdout)")
	fs.StringVar(output, "o", "sitemap.xml", "shorthand for -output")
	discoverOut := fs.String("discover-output", "", "write page discovery report as JSON")
	timeout := fs.Duration("timeout", 15*time.Second, "per-request timeout")
	runTimeout := fs.Duration("run-timeout", 10*time.Minute, "whole-crawl timeout (0 = none)")
	delay := fs.Duration("delay", 0, "delay between requests")
	maxBody := fs.Int64("max-body", 10<<20, "maximum response body size in bytes")
	userAgent := fs.String("user-agent", "creeper/"+version+" (+https://github.com/velox0/creeper)", "HTTP User-Agent")
	query := fs.Bool("include-query", false, "treat query strings as distinct pages")
	quiet := fs.Bool("quiet", false, "suppress progress and summary output")
	fs.BoolVar(quiet, "shh", false, "alias for -quiet")
	fs.BoolVar(quiet, "s", false, "shorthand for -quiet")
	verbose := fs.Bool("verbose", false, "include sitemap priority in summary")
	fs.BoolVar(verbose, "v", false, "shorthand for -verbose")
	track := fs.Bool("track", false, "track content changes across runs")
	fs.BoolVar(track, "t", false, "shorthand for -track")
	historyPath := fs.String("history", "", "change history file (default: per-host user data file)")
	port := fs.Int("port", 0, "fetch from localhost port while publishing original URLs")
	fs.IntVar(port, "p", 0, "shorthand for -port")
	failBroken := fs.Bool("fail-on-broken", false, "exit 2 if any requested page fails")
	showVersion := fs.Bool("version", false, "print version and exit")
	legacyXML := fs.Bool("xml", true, "deprecated compatibility flag; sitemap output is enabled by default")
	fs.BoolVar(legacyXML, "x", true, "deprecated shorthand")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, version)
		return 0
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: creeper [flags] <url>")
		fs.PrintDefaults()
		return 2
	}
	if *pages < 1 || *depth < 0 || *timeout <= 0 || *maxBody < 1 || *port < 0 || *port > 65535 {
		fmt.Fprintln(stderr, "creeper: pages, timeout, max-body, depth, or port has an invalid value")
		return 2
	}
	start, err := parseStartURL(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, "creeper:", err)
		return 2
	}
	if *runTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *runTimeout)
		defer cancel()
	}
	client := &http.Client{Timeout: *timeout}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		if *port != 0 {
			if req.URL.Host != fmt.Sprintf("localhost:%d", *port) {
				return fmt.Errorf("redirect left localhost crawl target")
			}
			req.Host = start.Host
			return nil
		}
		if !strings.EqualFold(req.URL.Host, start.Host) {
			return fmt.Errorf("redirect left crawl host %s", start.Host)
		}
		return nil
	}
	opts := CrawlOptions{MaxPages: *pages, MaxDepth: *depth, RequestDelay: *delay, MaxBodyBytes: *maxBody, UserAgent: *userAgent, IncludeQuery: *query, IncludeHosts: map[string]bool{strings.ToLower(start.Host): true}, Fetch: fetchConfig{useLocalhost: *port != 0, port: *port, origHost: start.Host}, Client: client}
	var history *ChangeHistory
	histFile := *historyPath
	if *track {
		if histFile == "" {
			histFile = historyFilename(start.Host)
		}
		history, err = loadHistory(histFile)
		if err != nil {
			fmt.Fprintf(stderr, "creeper: load history: %v\n", err)
			return 1
		}
	}
	state, err := crawl(ctx, start, opts, history)
	if err != nil {
		fmt.Fprintf(stderr, "creeper: crawl: %v\n", err)
		return 1
	}
	if len(state.Pages) == 0 {
		fmt.Fprintln(stderr, "creeper: crawl produced no valid HTML pages")
		printErrors(stderr, state.Errors)
		return 1
	}
	priorities := computePriorities(state, history)
	if *legacyXML {
		if err := writeXML(state, *output, priorities, stdout); err != nil {
			fmt.Fprintf(stderr, "creeper: write sitemap: %v\n", err)
			return 1
		}
	}
	if *discoverOut != "" {
		if err := writeDiscovery(state, *discoverOut); err != nil {
			fmt.Fprintf(stderr, "creeper: write discovery report: %v\n", err)
			return 1
		}
	}
	if history != nil {
		if err := saveHistory(history, histFile); err != nil {
			fmt.Fprintf(stderr, "creeper: save history: %v\n", err)
			return 1
		}
	}
	if !*quiet {
		fmt.Fprintf(stderr, "Crawled %d page(s), %d error(s); sitemap: %s\n", len(state.Pages), len(state.Errors), *output)
		printSummaryTable(stderr, state, normalizeURL(start, *query), priorities, *verbose)
		printErrors(stderr, state.Errors)
	}
	if *failBroken && len(state.Errors) > 0 {
		return 2
	}
	return 0
}

func parseStartURL(raw string) (*url.URL, error) {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid HTTP(S) URL %q", raw)
	}
	u.Fragment = ""
	return u, nil
}
func printErrors(w io.Writer, errors []CrawlError) {
	for _, e := range errors {
		fmt.Fprintf(w, "warning: %s: %s\n", e.URL, e.Error)
	}
}
