package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PageRecord is a single observation of a page's content hash at a point in time.
type PageRecord struct {
	Timestamp time.Time `json:"ts"`
	Hash      string    `json:"hash"`
}

// PageHistory holds the full observation history for a single URL.
type PageHistory struct {
	Records []PageRecord `json:"records"`
}

// ChangeHistory is the persistent store of all page histories, keyed by
// normalized URL. It is loaded from and saved to a per-host JSON file.
type ChangeHistory struct {
	Pages map[string]*PageHistory `json:"pages"`
}

// historyFilename returns the path to the per-host history file stored under
// ~/.creeper/. Falls back to .creeper/ in the working directory if the home
// directory cannot be determined.
func historyFilename(host string) string {
	safe := strings.NewReplacer(":", "_", "/", "_").Replace(host)
	name := fmt.Sprintf("%s.json", safe)
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".creeper", name)
	}
	return filepath.Join(home, ".creeper", name)
}

// loadHistory reads the history file for the given path, returning an empty
// ChangeHistory when the file does not exist or cannot be parsed.
func loadHistory(filename string) (*ChangeHistory, error) {
	h := &ChangeHistory{Pages: make(map[string]*PageHistory)}
	data, err := os.ReadFile(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return h, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, h); err != nil {
		return nil, err
	}
	if h.Pages == nil {
		h.Pages = make(map[string]*PageHistory)
	}
	return h, nil
}

// saveHistory writes h to filename, creating parent directories as needed.
func saveHistory(h *ChangeHistory, filename string) error {
	return atomicOutput(filename, io.Discard, func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(h)
	})
}

// record appends a new content observation for normURL. Returns true if the
// content differs from the most recent recorded hash.
func (h *ChangeHistory) record(normURL string, body []byte) bool {
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])

	ph, ok := h.Pages[normURL]
	if !ok {
		ph = &PageHistory{}
		h.Pages[normURL] = ph
	}
	changed := len(ph.Records) == 0 || ph.Records[len(ph.Records)-1].Hash != digest
	ph.Records = append(ph.Records, PageRecord{
		Timestamp: time.Now().UTC(),
		Hash:      digest,
	})
	const maxRecordsPerPage = 1000
	if len(ph.Records) > maxRecordsPerPage {
		ph.Records = ph.Records[len(ph.Records)-maxRecordsPerPage:]
	}
	return changed
}

// score computes a 0–1 value reflecting how often, how recently, and how
// consistently a page changes across its observation history.
//
// Three signals are combined:
//
//   - Frequency  (50%): fraction of consecutive observations where content changed.
//   - Recency    (30%): exponential decay from the last change; half-life = 30 days.
//   - Consistency(20%): regularity of change intervals measured as 1 − min(CV, 1),
//     where CV is the coefficient of variation (stddev/mean) of inter-change durations.
//     A perfectly regular schedule scores 1; a chaotic one scores 0.
func (ph *PageHistory) score() float64 {
	n := len(ph.Records)
	if n < 2 {
		return 0
	}

	// Collect timestamps of change events.
	var changeTimes []time.Time
	for i := 1; i < n; i++ {
		if ph.Records[i].Hash != ph.Records[i-1].Hash {
			changeTimes = append(changeTimes, ph.Records[i].Timestamp)
		}
	}
	if len(changeTimes) == 0 {
		return 0
	}

	// 1. Frequency: proportion of observations that showed a change.
	frequencyScore := float64(len(changeTimes)) / float64(n-1)

	// 2. Recency: exponential decay from last change, half-life 30 days.
	daysSince := time.Since(changeTimes[len(changeTimes)-1]).Hours() / 24
	recencyScore := math.Exp(-math.Log(2) * daysSince / 30.0)

	// 3. Consistency: low coefficient of variation across inter-change intervals.
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
			cv := math.Sqrt(variance) / mean
			consistencyScore = math.Max(0, 1-math.Min(1, cv))
		}
	}

	return 0.5*frequencyScore + 0.3*recencyScore + 0.2*consistencyScore
}
