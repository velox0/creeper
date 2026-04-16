package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"golang.org/x/net/html"
)

// CrawlerState holds all mutable state accumulated during a crawl.
type CrawlerState struct {
	visited      map[string]bool
	incoming     map[string]int
	paths        map[string]string // normalized url -> path only
	outgoing     map[string]int    // normalized url -> outgoing link count
	maxPages     int
	maxDepth     int
	pagesVisited int
}

func newCrawlerState(normStart, startPath string, maxPages, maxDepth int) *CrawlerState {
	return &CrawlerState{
		visited:      map[string]bool{normStart: true},
		incoming:     map[string]int{normStart: 0},
		paths:        map[string]string{normStart: startPath},
		outgoing:     map[string]int{normStart: 0},
		maxPages:     maxPages,
		maxDepth:     maxDepth,
		pagesVisited: 1,
	}
}

// fetchConfig bundles the parameters needed to resolve localhost rewrites.
type fetchConfig struct {
	useLocalhost bool
	port         int
	origHost     string
}

// fetch retrieves a URL, returning the raw body bytes and the parsed HTML tree.
// When cfg.useLocalhost is true the request is rewritten to hit localhost while
// sending the original Host header, which is useful for local dev-server testing.
func fetch(u *url.URL, cfg fetchConfig) ([]byte, *html.Node, error) {
	fetchURL := u.String()
	if cfg.useLocalhost {
		local := *u
		local.Scheme = "http"
		local.Host = "localhost"
		if cfg.port != 80 {
			local.Host = fmt.Sprintf("localhost:%d", cfg.port)
		}
		fetchURL = local.String()
	}

	req, err := http.NewRequest(http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, nil, err
	}
	if cfg.useLocalhost {
		req.Host = cfg.origHost
	}

	resp, err := http.DefaultClient.Do(req)
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

// normalizeURL strips query params and fragments, ensuring a non-empty path.
func normalizeURL(u *url.URL) string {
	norm := *u
	norm.RawQuery = ""
	norm.Fragment = ""
	if norm.Path == "" {
		norm.Path = "/"
	}
	return norm.String()
}

// pathOnly returns the escaped path of u, defaulting to "/" when empty.
func pathOnly(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	return p
}

// visitLinks walks the HTML tree rooted at n, recursively fetching and visiting
// every in-domain anchor link it encounters.
func visitLinks(base *url.URL, n *html.Node, state *CrawlerState, from string, depth int, cfg fetchConfig, history *ChangeHistory) {
	if state.pagesVisited >= state.maxPages || (state.maxDepth > 0 && depth > state.maxDepth) {
		return
	}
	if n.Type == html.ElementNode && n.Data == "a" {
		for _, attr := range n.Attr {
			if attr.Key != "href" {
				continue
			}
			u, err := url.Parse(attr.Val)
			if err != nil {
				continue
			}
			absURL := base.ResolveReference(u)
			if absURL.Host != base.Host || (absURL.Scheme != "http" && absURL.Scheme != "https") {
				continue
			}

			normStr := normalizeURL(absURL)
			if normStr != from {
				state.incoming[normStr]++
			}
			state.paths[normStr] = pathOnly(absURL)
			state.outgoing[from]++

			if !state.visited[normStr] && state.pagesVisited < state.maxPages {
				fmt.Println(normStr)
				state.visited[normStr] = true
				state.pagesVisited++

				body, doc, err := fetch(absURL, cfg)
				if err == nil {
					if history != nil {
						history.record(normStr, body)
					}
					visitLinks(base, doc, state, normStr, depth+1, cfg, history)
				}
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		visitLinks(base, c, state, from, depth, cfg, history)
	}
}
