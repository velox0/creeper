package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/html"
)

type Page struct {
	URL      string `json:"url"`
	Path     string `json:"path"`
	Status   int    `json:"status"`
	Depth    int    `json:"depth"`
	Incoming int    `json:"incoming"`
	Outgoing int    `json:"outgoing"`
	Title    string `json:"title,omitempty"`
	LastMod  string `json:"last_modified,omitempty"`
	Body     []byte `json:"-"`
}
type CrawlError struct {
	URL    string `json:"url"`
	Status int    `json:"status,omitempty"`
	Error  string `json:"error"`
}
type CrawlerState struct {
	Pages  map[string]*Page `json:"-"`
	Errors []CrawlError     `json:"errors,omitempty"`
}
type CrawlOptions struct {
	MaxPages     int
	MaxDepth     int
	RequestDelay time.Duration
	MaxBodyBytes int64
	UserAgent    string
	IncludeQuery bool
	IncludeHosts map[string]bool
	Fetch        fetchConfig
	Client       *http.Client
}
type fetchConfig struct {
	useLocalhost bool
	port         int
	origHost     string
}
type queuedURL struct {
	u     *url.URL
	depth int
}

func crawl(ctx context.Context, start *url.URL, opts CrawlOptions, history *ChangeHistory) (*CrawlerState, error) {
	state := &CrawlerState{Pages: make(map[string]*Page)}
	startNorm := normalizeURL(start, opts.IncludeQuery)
	queue := []queuedURL{{u: start, depth: 0}}
	seen := map[string]bool{startNorm: true}
	incoming := map[string]int{startNorm: 0}
	requests := 0
	for len(queue) > 0 && len(state.Pages) < opts.MaxPages {
		if err := ctx.Err(); err != nil {
			return state, err
		}
		item := queue[0]
		queue = queue[1:]
		norm := normalizeURL(item.u, opts.IncludeQuery)
		if opts.RequestDelay > 0 && requests > 0 {
			select {
			case <-time.After(opts.RequestDelay):
			case <-ctx.Done():
				return state, ctx.Err()
			}
		}
		requests++
		body, doc, resp, err := fetch(ctx, item.u, opts)
		if err != nil {
			ce := CrawlError{URL: norm, Error: err.Error()}
			if resp != nil {
				ce.Status = resp.StatusCode
			}
			state.Errors = append(state.Errors, ce)
			continue
		}
		page := &Page{URL: norm, Path: pathOnly(item.u), Status: resp.StatusCode, Depth: item.depth, Incoming: incoming[norm], Body: body, Title: documentTitle(doc), LastMod: httpLastModified(resp)}
		state.Pages[norm] = page
		if history != nil {
			history.record(norm, body)
		}
		if opts.MaxDepth > 0 && item.depth >= opts.MaxDepth {
			continue
		}
		links := extractLinks(item.u, doc, opts)
		page.Outgoing = len(links)
		for _, link := range links {
			linkNorm := normalizeURL(link, opts.IncludeQuery)
			if linkNorm != norm {
				incoming[linkNorm]++
			}
			if p := state.Pages[linkNorm]; p != nil {
				p.Incoming = incoming[linkNorm]
			}
			if !seen[linkNorm] {
				seen[linkNorm] = true
				queue = append(queue, queuedURL{u: link, depth: item.depth + 1})
			}
		}
	}
	return state, nil
}

func fetch(ctx context.Context, u *url.URL, opts CrawlOptions) ([]byte, *html.Node, *http.Response, error) {
	fetchURL := *u
	if opts.Fetch.useLocalhost {
		fetchURL.Scheme = "http"
		fetchURL.Host = fmt.Sprintf("localhost:%d", opts.Fetch.port)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL.String(), nil)
	if err != nil {
		return nil, nil, nil, err
	}
	req.Header.Set("User-Agent", opts.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	if opts.Fetch.useLocalhost {
		req.Host = opts.Fetch.origHost
	}
	resp, err := opts.Client.Do(req)
	if err != nil {
		return nil, nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, nil, resp, fmt.Errorf("HTTP %s", resp.Status)
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if contentType != "" && !strings.Contains(contentType, "text/html") && !strings.Contains(contentType, "application/xhtml+xml") {
		return nil, nil, resp, fmt.Errorf("unsupported content type %q", contentType)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, opts.MaxBodyBytes+1))
	if err != nil {
		return nil, nil, resp, err
	}
	if int64(len(body)) > opts.MaxBodyBytes {
		return nil, nil, resp, fmt.Errorf("body exceeds %d bytes", opts.MaxBodyBytes)
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, nil, resp, err
	}
	return body, doc, resp, nil
}

func extractLinks(base *url.URL, doc *html.Node, opts CrawlOptions) []*url.URL {
	unique := make(map[string]*url.URL)
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if strings.EqualFold(a.Key, "href") {
					u, err := url.Parse(strings.TrimSpace(a.Val))
					if err != nil {
						break
					}
					u = base.ResolveReference(u)
					u.Host = strings.ToLower(u.Host)
					if (u.Scheme == "http" || u.Scheme == "https") && opts.IncludeHosts[u.Host] {
						u.Fragment = ""
						if !opts.IncludeQuery {
							u.RawQuery = ""
						}
						unique[normalizeURL(u, opts.IncludeQuery)] = u
					}
					break
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	keys := make([]string, 0, len(unique))
	for k := range unique {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*url.URL, 0, len(keys))
	for _, k := range keys {
		out = append(out, unique[k])
	}
	return out
}

func normalizeURL(u *url.URL, includeQuery bool) string {
	n := *u
	n.Fragment = ""
	n.Scheme = strings.ToLower(n.Scheme)
	n.Host = strings.ToLower(n.Host)
	if !includeQuery {
		n.RawQuery = ""
	}
	if n.Path == "" {
		n.Path = "/"
	}
	return n.String()
}
func pathOnly(u *url.URL) string {
	if u.EscapedPath() == "" {
		return "/"
	}
	return u.EscapedPath()
}
func documentTitle(doc *html.Node) string {
	var title string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if title != "" {
			return
		}
		if n.Type == html.ElementNode && n.Data == "title" && n.FirstChild != nil {
			title = strings.TrimSpace(n.FirstChild.Data)
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return title
}
func httpLastModified(resp *http.Response) string {
	if t, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	return ""
}
