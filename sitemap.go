package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type xmlURLSet struct {
	XMLName xml.Name `xml:"urlset"`
	Xmlns   string   `xml:"xmlns,attr"`
	URLs    []xmlURL `xml:"url"`
}
type xmlURL struct {
	Loc      string `xml:"loc"`
	LastMod  string `xml:"lastmod,omitempty"`
	Priority string `xml:"priority"`
}

func pathDepth(path string) int {
	s := strings.Trim(path, "/")
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "/"))
}
func computePriorities(state *CrawlerState, history *ChangeHistory) map[string]float64 {
	maxIncoming := 0
	for _, p := range state.Pages {
		if p.Incoming > maxIncoming {
			maxIncoming = p.Incoming
		}
	}
	raw, result, maxRaw := map[string]float64{}, map[string]float64{}, 0.0
	for norm, page := range state.Pages {
		inScore := 0.0
		if maxIncoming > 0 {
			inScore = math.Log1p(float64(page.Incoming)) / math.Log1p(float64(maxIncoming))
		}
		depthScore := 1 / math.Pow(2, float64(pathDepth(page.Path)))
		value := .7*inScore + .3*depthScore
		if history != nil && history.Pages[norm] != nil {
			value = .55*inScore + .2*depthScore + .25*history.Pages[norm].score()
		}
		raw[norm] = value
		if value > maxRaw {
			maxRaw = value
		}
	}
	for norm, value := range raw {
		if maxRaw > 0 {
			value /= maxRaw
		} else {
			value = 1
		}
		result[norm] = math.Max(.1, math.Min(1, value))
	}
	return result
}
func writeXML(state *CrawlerState, filename string, priorities map[string]float64, stdout io.Writer) error {
	entries := make([]xmlURL, 0, len(state.Pages))
	for norm, page := range state.Pages {
		entries = append(entries, xmlURL{Loc: norm, LastMod: page.LastMod, Priority: fmt.Sprintf("%.2f", priorities[norm])})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Loc < entries[j].Loc })
	if len(entries) > 50000 {
		return fmt.Errorf("%d URLs exceeds the sitemap protocol limit of 50000", len(entries))
	}
	return atomicOutput(filename, stdout, func(w io.Writer) error {
		if _, err := io.WriteString(w, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"); err != nil {
			return err
		}
		enc := xml.NewEncoder(w)
		enc.Indent("", "  ")
		return enc.Encode(xmlURLSet{Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9", URLs: entries})
	})
}
func atomicOutput(filename string, stdout io.Writer, write func(io.Writer) error) error {
	if filename == "-" {
		return write(stdout)
	}
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".creeper-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(tmp)
		}
	}()
	if err = write(f); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, filename); err != nil {
		return err
	}
	ok = true
	return nil
}
