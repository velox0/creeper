package main

import (
	"encoding/xml"
	"flag"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/term"
)

// fetches the HTML document from the given URL
func fetch(url string) (*html.Node, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return html.Parse(resp.Body)
}

// normalizeURL removes query params and fragments, returns scheme://host/path
func normalizeURL(u *url.URL) string {
	norm := *u
	norm.RawQuery = ""
	norm.Fragment = ""
	if norm.Path == "" {
		norm.Path = "/"
	}
	return norm.String()
}

// pathOnly returns just the path from a URL string
func pathOnly(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	return p
}

type XMLUrlSet struct {
	XMLName xml.Name `xml:"urlset"`
	Xmlns   string   `xml:"xmlns,attr"`
	Urls    []XMLUrl `xml:"url"`
}
type XMLUrl struct {
	Loc      string `xml:"loc"`
	Priority string `xml:"priority"`
}

// CrawlerState holds crawling state and statistics
type CrawlerState struct {
	visited      map[string]bool
	incoming     map[string]int
	maxPages     int
	pagesVisited int
	maxDepth     int
	paths        map[string]string // normalized url -> path only
	outgoing     map[string]int    // normalized url -> outgoing link count
}

// visitLinks finds all in-domain links and recursively visits them
func visitLinks(base *url.URL, n *html.Node, state *CrawlerState, from string, depth int, useLocalhost bool, port int, origHost string) {
	if state.pagesVisited >= state.maxPages || (state.maxDepth > 0 && depth > state.maxDepth) {
		return
	}
	if n.Type == html.ElementNode && n.Data == "a" {
		for _, attr := range n.Attr {
			if attr.Key == "href" {
				href := attr.Val
				u, err := url.Parse(href)
				if err != nil {
					continue
				}
				absURL := base.ResolveReference(u)
				if absURL.Host == base.Host && (absURL.Scheme == "http" || absURL.Scheme == "https") {
					normStr := normalizeURL(absURL)
					fromNorm := from
					if fromNorm != normStr {
						state.incoming[normStr]++
					}
					state.paths[normStr] = pathOnly(absURL)
					state.outgoing[fromNorm]++
					if !state.visited[normStr] && state.pagesVisited < state.maxPages {
						fmt.Println(normStr)
						state.visited[normStr] = true
						state.pagesVisited++
						doc, err := fetchWithHost(absURL, useLocalhost, port, origHost)
						if err == nil {
							visitLinks(base, doc, state, normStr, depth+1, useLocalhost, port, origHost)
						}
					}
				}
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		visitLinks(base, c, state, from, depth, useLocalhost, port, origHost)
	}
}

func printSummaryTable(state *CrawlerState, startPath string) {
	// Get terminal width
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width < 30 {
		width = 80
	}
	countCol := 6
	pathCol := width - countCol - 3

	paths := make([]string, 0, len(state.paths))
	for _, p := range state.paths {
		paths = append(paths, p)
	}
	uniquePaths := make(map[string]struct{})
	for _, p := range paths {
		uniquePaths[p] = struct{}{}
	}
	finalPaths := make([]string, 0, len(uniquePaths))
	for p := range uniquePaths {
		finalPaths = append(finalPaths, p)
	}
	startIdx := -1
	for i, p := range finalPaths {
		if p == startPath {
			startIdx = i
			break
		}
	}
	if startIdx > 0 {
		finalPaths[0], finalPaths[startIdx] = finalPaths[startIdx], finalPaths[0]
	}
	if startIdx != 0 {
		sort.Strings(finalPaths[1:])
	} else {
		sort.Strings(finalPaths[1:])
	}

	// Header
	fmt.Printf("%-*s | %s\n", countCol, "Count", "Path")
	fmt.Println(strings.Repeat("-", width))

	for _, p := range finalPaths {
		// Find normalized url for this path
		var incoming int
		for norm, path := range state.paths {
			if path == p {
				incoming = state.incoming[norm]
				break
			}
		}
		// Split path into chunks
		pathRunes := []rune(p)
		for i := 0; i < len(pathRunes); i += pathCol {
			end := i + pathCol
			if end > len(pathRunes) {
				end = len(pathRunes)
			}
			chunk := string(pathRunes[i:end])
			if i == 0 {
				fmt.Printf("%-*d | %s\n", countCol, incoming, chunk)
			} else {
				fmt.Printf("%-*s | %s\n", countCol, "", chunk)
			}
		}
	}
}

// pathDepth returns the number of path segments (e.g. "/" -> 0, "/a/b" -> 2)
func pathDepth(path string) int {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "/"))
}

func writeXML(state *CrawlerState, outFile string) error {
	maxIncoming := 0
	for norm := range state.paths {
		if state.incoming[norm] > maxIncoming {
			maxIncoming = state.incoming[norm]
		}
	}

	urls := make([]XMLUrl, 0, len(state.paths))
	for norm := range state.paths {
		in := state.incoming[norm]

		// Log-scaled incoming link score: pages with more backlinks rank higher
		var inScore float64
		if maxIncoming > 0 {
			inScore = math.Log1p(float64(in)) / math.Log1p(float64(maxIncoming))
		}

		// Depth score: shallower pages are more important (halves per level)
		depth := pathDepth(state.paths[norm])
		depthScore := 1.0 / math.Pow(2.0, float64(depth))

		// 70% incoming links, 30% depth; clamp to sitemap spec [0.1, 1.0]
		priority := 0.7*inScore + 0.3*depthScore
		priority = math.Max(0.1, math.Min(1.0, priority))

		urls = append(urls, XMLUrl{
			Loc:      norm,
			Priority: fmt.Sprintf("%.2f", priority),
		})
	}
	sort.Slice(urls, func(i, j int) bool {
		return urls[i].Priority > urls[j].Priority
	})
	urlset := XMLUrlSet{
		Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9",
		Urls:  urls,
	}
	f, err := os.Create(outFile)
	if err != nil {
		return err
	}
	defer f.Close()
	f.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	f.WriteString("<!-- Generated by github.com/velox0/creeper -->\n")
	enc := xml.NewEncoder(f)
	enc.Indent("", "  ")
	return enc.Encode(urlset)
}

func fetchWithHost(u *url.URL, useLocalhost bool, port int, origHost string) (*html.Node, error) {
	client := &http.Client{}
	fetchURL := u.String()
	if useLocalhost {
		// Rewrite URL to use localhost and port, and always use http
		localURL := *u
		localURL.Scheme = "http"
		localURL.Host = "localhost"
		if port != 80 {
			localURL.Host = fmt.Sprintf("localhost:%d", port)
		}
		fetchURL = localURL.String()
	}
	req, err := http.NewRequest("GET", fetchURL, nil)
	if err != nil {
		return nil, err
	}
	if useLocalhost {
		req.Host = origHost
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return html.Parse(resp.Body)
}

func main() {
	maxPages := flag.Int("n", 100, "Maximum number of pages to visit")
	maxDepth := flag.Int("i", 0, "Maximum recursion depth (0 = unlimited)")
	showSummary := flag.Bool("s", true, "Show summary of incoming links at the end")
	xmlOut := flag.Bool("x", false, "Generate XML sitemap")
	outFile := flag.String("o", "sitemap.xml", "Output file for XML sitemap (used with -x)")
	useLocalhost := flag.Bool("l", false, "Crawl using localhost for the given domain (for local server testing)")
	port := flag.Int("p", 80, "Port to use when crawling localhost")
	flag.Parse()

	var startURL string
	if flag.NArg() < 1 {
		// No URL given — default to localhost on the specified port
		startURL = fmt.Sprintf("http://localhost:%d", *port)
		*useLocalhost = false // crawling localhost directly, no rewriting needed
	} else {
		startURL = flag.Arg(0)
		// If a non-default port is given without -l, assume localhost mode
		if *port != 80 && !*useLocalhost {
			*useLocalhost = true
		}
	}

	// Default to https if no scheme is specified (skip for localhost)
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
	origHost := base.Host
	firstURL := base
	if *useLocalhost {
		localURL := *base
		localURL.Scheme = "http"
		localURL.Host = "localhost"
		if *port != 80 {
			localURL.Host = fmt.Sprintf("localhost:%d", *port)
		}
		firstURL = &localURL
	}
	doc, err := fetchWithHost(firstURL, *useLocalhost, *port, origHost)
	if err != nil {
		fmt.Println("Error fetching URL:", err)
		return
	}
	normStart := normalizeURL(base)
	startPath := pathOnly(base)
	state := &CrawlerState{
		visited:      map[string]bool{normStart: true},
		incoming:     map[string]int{normStart: 0},
		maxPages:     *maxPages,
		pagesVisited: 1,
		maxDepth:     *maxDepth,
		paths:        map[string]string{normStart: startPath},
		outgoing:     map[string]int{normStart: 0},
	}
	visitLinks(base, doc, state, normStart, 1, *useLocalhost, *port, origHost)

	if *showSummary {
		fmt.Println("\nSummary of visited pages and incoming link counts:")
		printSummaryTable(state, startPath)
	}
	if *xmlOut {
		err := writeXML(state, *outFile)
		if err != nil {
			fmt.Println("Error writing XML:", err)
		} else {
			fmt.Printf("XML written to %s\n", *outFile)
		}
	}
}
