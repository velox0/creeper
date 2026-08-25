package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

type discoveryReport struct {
	Pages  []*Page      `json:"pages"`
	Errors []CrawlError `json:"errors,omitempty"`
}

func writeDiscovery(state *CrawlerState, filename string) error {
	pages := make([]*Page, 0, len(state.Pages))
	for _, p := range state.Pages {
		clone := *p
		clone.Body = nil
		pages = append(pages, &clone)
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].URL < pages[j].URL })
	return atomicOutput(filename, io.Discard, func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(discoveryReport{Pages: pages, Errors: state.Errors})
	})
}
func printSummaryTable(w io.Writer, state *CrawlerState, start string, priorities map[string]float64, verbose bool) {
	pages := make([]*Page, 0, len(state.Pages))
	for _, p := range state.Pages {
		pages = append(pages, p)
	}
	sort.Slice(pages, func(i, j int) bool {
		if pages[i].URL == start {
			return true
		}
		if pages[j].URL == start {
			return false
		}
		return pages[i].URL < pages[j].URL
	})
	for _, p := range pages {
		if verbose {
			fmt.Fprintf(w, "%4d  %.2f  %s\n", p.Incoming, priorities[p.URL], p.URL)
		} else {
			fmt.Fprintf(w, "%4d  %s\n", p.Incoming, p.URL)
		}
	}
}
