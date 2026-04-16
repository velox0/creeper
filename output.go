package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"golang.org/x/term"
)

// printSummaryTable writes a two-column table (incoming-link count | path) to
// stdout, sized to the current terminal width.
func printSummaryTable(state *CrawlerState, startPath string) {
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width < 30 {
		width = 80
	}
	const countCol = 6
	pathCol := width - countCol - 3

	// Deduplicate paths and pin startPath to the top.
	seen := make(map[string]struct{}, len(state.paths))
	for _, p := range state.paths {
		seen[p] = struct{}{}
	}
	sorted := make([]string, 0, len(seen))
	for p := range seen {
		sorted = append(sorted, p)
	}
	// Move startPath to index 0, then sort the rest.
	for i, p := range sorted {
		if p == startPath {
			sorted[0], sorted[i] = sorted[i], sorted[0]
			break
		}
	}
	sort.Strings(sorted[1:])

	fmt.Printf("%-*s | %s\n", countCol, "Count", "Path")
	fmt.Println(strings.Repeat("-", width))

	for _, p := range sorted {
		var incoming int
		for norm, path := range state.paths {
			if path == p {
				incoming = state.incoming[norm]
				break
			}
		}
		runes := []rune(p)
		for i := 0; i < len(runes); i += pathCol {
			end := i + pathCol
			if end > len(runes) {
				end = len(runes)
			}
			chunk := string(runes[i:end])
			if i == 0 {
				fmt.Printf("%-*d | %s\n", countCol, incoming, chunk)
			} else {
				fmt.Printf("%-*s | %s\n", countCol, "", chunk)
			}
		}
	}
}
