package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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

// PageRecord is a single observation of a page's content hash at a point in time.
type PageRecord struct {
	Timestamp time.Time `json:"ts"`
	Hash      string    `json:"hash"`
}

// PageHistory holds the full observation history for a single URL.
type PageHistory struct {
	Records []PageRecord `json:"records"`
}

// ChangeHistory is the persistent store of all page histories keyed by normalized URL.
type ChangeHistory struct {
	Pages map[string]*PageHistory `json:"pages"`
}

func historyFilename(host string) string {
	safe := strings.NewReplacer(":", "_", "/", "_").Replace(host)
	name := fmt.Sprintf("%s.json", safe)
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".creeper", name)
	}
	return filepath.Join(home, ".creeper", name)
}

func loadHistory(filename string) *ChangeHistory {
	h := &ChangeHistory{Pages: make(map[string]*PageHistory)}
	data, err := os.ReadFile(filename)
	if err != nil {
		return h // first run or file missing
	}
	_ = json.Unmarshal(data, h)
	if h.Pages == nil {
		h.Pages = make(map[string]*PageHistory)
	}
	return h
}

func saveHistory(h *ChangeHistory, filename string) error {
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		return err
	}
	return os.WriteFile(filename, data, 0644)
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// recordHash appends a new observation for normURL. Returns true if the content changed.
func recordHash(h *ChangeHistory, normURL string, data []byte) bool {
	digest := hashBytes(data)
	ph, ok := h.Pages[normURL]
	if !ok {
		ph = &PageHistory{}
		h.Pages[normURL] = ph
	}
	changed := len(ph.Records) == 0 || ph.Records[len(ph.Records)-1].Hash != digest
	ph.Records = append(ph.Records, PageRecord{Timestamp: time.Now().UTC(), Hash: digest})
	return changed
}

// changeScore computes a 0–1 score reflecting how often, how recently, and how
// consistently a page changes across its observation history.
//
// Algorithm:
//   - Frequency  (50%): fraction of consecutive observations where content changed
//   - Recency    (30%): exponential decay from last change; half-life = 30 days
//   - Consistency(20%): regularity of change intervals; 1 - min(CV, 1)
//     where CV = stddev/mean of inter-change intervals
func changeScore(ph *PageHistory) float64 {
	n := len(ph.Records)
	if n < 2 {
		return 0
	}

	// Identify change events (consecutive records with different hashes)
	var changeTimes []time.Time
	for i := 1; i < n; i++ {
		if ph.Records[i].Hash != ph.Records[i-1].Hash {
			changeTimes = append(changeTimes, ph.Records[i].Timestamp)
		}
	}
	if len(changeTimes) == 0 {
		return 0
	}

	// 1. Frequency: proportion of observations that showed a change
	frequencyScore := float64(len(changeTimes)) / float64(n-1)

	// 2. Recency: exponential decay from last change, half-life 30 days
	daysSince := time.Since(changeTimes[len(changeTimes)-1]).Hours() / 24
	recencyScore := math.Exp(-math.Log(2) * daysSince / 30.0)

	// 3. Consistency: low coefficient of variation across inter-change intervals
	consistencyScore := 0.0
	if len(changeTimes) >= 2 {
		intervals := make([]float64, len(changeTimes)-1)
		for i := 1; i < len(changeTimes); i++ {
			intervals[i-1] = changeTimes[i].Sub(changeTimes[i-1]).Hours()
		}
		var mean float64
		for _, iv := range intervals {
			mean += iv
		}
		mean /= float64(len(intervals))
		if mean > 0 {
			var variance float64
			for _, iv := range intervals {
				d := iv - mean
				variance += d * d
			}
			variance /= float64(len(intervals))
			cv := math.Sqrt(variance) / mean // coefficient of variation
			consistencyScore = math.Max(0, 1-math.Min(1, cv))
		}
	}

	return 0.5*frequencyScore + 0.3*recencyScore + 0.2*consistencyScore
}

// visitLinks finds all in-domain links and recursively visits them
func visitLinks(base *url.URL, n *html.Node, state *CrawlerState, from string, depth int, useLocalhost bool, port int, origHost string, history *ChangeHistory) {
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
						body, doc, err := fetchWithHost(absURL, useLocalhost, port, origHost)
						if err == nil {
							if history != nil {
								recordHash(history, normStr, body)
							}
							visitLinks(base, doc, state, normStr, depth+1, useLocalhost, port, origHost, history)
						}
					}
				}
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		visitLinks(base, c, state, from, depth, useLocalhost, port, origHost, history)
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

// writeXML generates the XML sitemap. When history is non-nil the priority
// formula blends in a change score (how often/recently/consistently pages change).
//
// Without history: priority = 0.70*inScore + 0.30*depthScore
// With    history: priority = 0.55*inScore + 0.20*depthScore + 0.25*changeScore
func writeXML(state *CrawlerState, outFile string, history *ChangeHistory) (err error) {
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

		var priority float64
		if history != nil {
			if ph, ok := history.Pages[norm]; ok {
				cs := changeScore(ph)
				priority = 0.55*inScore + 0.20*depthScore + 0.25*cs
			} else {
				// URL has no history yet; fall back to standard formula
				priority = 0.7*inScore + 0.3*depthScore
			}
		} else {
			priority = 0.7*inScore + 0.3*depthScore
		}
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
	defer func() {
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
	}()
	if _, err := f.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"); err != nil {
		return err
	}
	if _, err := f.WriteString("<!-- Generated by github.com/velox0/creeper -->\n"); err != nil {
		return err
	}
	enc := xml.NewEncoder(f)
	enc.Indent("", "  ")
	if err := enc.Encode(urlset); err != nil {
		return err
	}
	return nil
}

// fetchWithHost fetches a URL, returning the raw response body and the parsed
// HTML tree. The raw bytes are used for content hashing when change tracking
// is enabled.
func fetchWithHost(u *url.URL, useLocalhost bool, port int, origHost string) ([]byte, *html.Node, error) {
	client := &http.Client{}
	fetchURL := u.String()
	if useLocalhost {
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
		return nil, nil, err
	}
	if useLocalhost {
		req.Host = origHost
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	return body, doc, nil
}

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
	useLocalhost := flag.Bool("local", false, "Crawl using localhost for the given domain (for local server testing, shorthand -l)")
	flag.BoolVar(useLocalhost, "l", false, "")
	port := flag.Int("port", 80, "Port to use when crawling localhost (shorthand -p)")
	flag.IntVar(port, "p", 80, "")
	trackChanges := flag.Bool("track", false, "Track page content changes across runs (shorthand -t, -u)")
	flag.BoolVar(trackChanges, "t", false, "")
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

	// Load change history when tracking is enabled
	var history *ChangeHistory
	var histFile string
	if *trackChanges {
		histFile = historyFilename(origHost)
		history = loadHistory(histFile)
		prev := len(history.Pages)
		if prev > 0 {
			fmt.Printf("Loaded change history for %d page(s) from %s\n", prev, histFile)
		}
	}

	body, doc, err := fetchWithHost(firstURL, *useLocalhost, *port, origHost)
	if err != nil {
		fmt.Println("Error fetching URL:", err)
		return
	}
	normStart := normalizeURL(base)
	startPath := pathOnly(base)

	if history != nil {
		recordHash(history, normStart, body)
	}

	state := &CrawlerState{
		visited:      map[string]bool{normStart: true},
		incoming:     map[string]int{normStart: 0},
		maxPages:     *maxPages,
		pagesVisited: 1,
		maxDepth:     *maxDepth,
		paths:        map[string]string{normStart: startPath},
		outgoing:     map[string]int{normStart: 0},
	}
	visitLinks(base, doc, state, normStart, 1, *useLocalhost, *port, origHost, history)

	// Persist updated history
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
		err := writeXML(state, *outFile, history)
		if err != nil {
			fmt.Println("Error writing XML:", err)
		} else {
			fmt.Printf("XML written to %s\n", *outFile)
		}
	}
}
