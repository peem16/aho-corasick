// bench2json parses `go test -bench` output from stdin and appends results
// to topic-specific JSON files in the results/ directory.
//
// Subcommands:
//
//	(none)           parse stdin → results/*.json
//	--view           print a summary table of the last run per topic file
//	--gen-history    read results/overlapping.json → scripts/history.json
//	                 (multi-series format ready for scripts/plot.js)
//	--clear-history  reset scripts/history.json to empty (keeps schema, wipes all runs)
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ─── Core result types ───────────────────────────────────────────────────────

// BenchmarkResult holds the parsed data from one benchmark line.
type BenchmarkResult struct {
	Name        string   `json:"name"`
	Procs       int      `json:"procs"`
	N           int      `json:"n"`
	NsPerOp     float64  `json:"ns_per_op"`
	MbPerS      *float64 `json:"mb_per_s"`    // null when b.SetBytes not called
	BytesPerOp  int64    `json:"bytes_per_op"`
	AllocsPerOp int64    `json:"allocs_per_op"`
}

// Run holds one benchmark session's metadata and all its results.
type Run struct {
	Timestamp  string            `json:"timestamp"`
	RunID      string            `json:"run_id"`
	Commit     string            `json:"commit,omitempty"` // git short SHA, if available
	GoVersion  string            `json:"go_version"`
	OS         string            `json:"os"`
	Arch       string            `json:"arch"`
	CPU        string            `json:"cpu"`
	Benchmarks []BenchmarkResult `json:"benchmarks"`
}

// ─── History types (for scripts/history.json) ────────────────────────────────

// HistoryRun is one X-axis point in the plot history file.
// Each map key is a series label (e.g. "NFA_Small", "DFA_Medium").
type HistoryRun struct {
	Label       string             `json:"label"`            // shown on X axis
	Commit      string             `json:"commit,omitempty"` // git SHA
	Timestamp   string             `json:"timestamp"`
	RunID       string             `json:"run_id"`
	NsPerOp     map[string]float64 `json:"ns_per_op"`
	BytesPerOp  map[string]float64 `json:"bytes_per_op"`
	AllocsPerOp map[string]float64 `json:"allocs_per_op"`
}

// History is the full plot-history file (scripts/history.json).
type History struct {
	Topic  string       `json:"topic"`
	Title  string       `json:"title"`
	Series []string     `json:"series"` // ordered list of series labels
	Runs   []HistoryRun `json:"runs"`
}

// ─── Entry point ─────────────────────────────────────────────────────────────

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--view":
			viewLastRuns()
			return
		case "--gen-history":
			if err := genHistory(); err != nil {
				fmt.Fprintf(os.Stderr, "bench2json: %v\n", err)
				os.Exit(1)
			}
			return
		case "--clear-history":
			if err := clearHistory(); err != nil {
				fmt.Fprintf(os.Stderr, "bench2json: %v\n", err)
				os.Exit(1)
			}
			return
		}
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "bench2json: %v\n", err)
		os.Exit(1)
	}
}

// ─── Capture: stdin → results/*.json ─────────────────────────────────────────

func run() error {
	now := time.Now().UTC()

	meta := struct {
		goVersion string
		os        string
		arch      string
		cpu       string
	}{
		goVersion: runtime.Version(),
		os:        runtime.GOOS,
		arch:      runtime.GOARCH,
		cpu:       fmt.Sprintf("%d logical CPUs", runtime.NumCPU()),
	}

	var results []BenchmarkResult

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "goos: "):
			meta.os = strings.TrimPrefix(line, "goos: ")
		case strings.HasPrefix(line, "goarch: "):
			meta.arch = strings.TrimPrefix(line, "goarch: ")
		case strings.HasPrefix(line, "cpu: "):
			meta.cpu = strings.TrimPrefix(line, "cpu: ")
		case strings.HasPrefix(line, "Benchmark"):
			r, err := parseBenchLine(line)
			if err == nil {
				results = append(results, r)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}
	if len(results) == 0 {
		fmt.Fprintln(os.Stderr, "bench2json: no benchmark results found in input")
		return nil
	}

	currentRun := Run{
		Timestamp:  now.Format(time.RFC3339),
		RunID:      now.Format("20060102T150405Z"),
		Commit:     gitShortSHA(),
		GoVersion:  meta.goVersion,
		OS:         meta.os,
		Arch:       meta.arch,
		CPU:        meta.cpu,
		Benchmarks: results,
	}

	if err := os.MkdirAll("results", 0755); err != nil {
		return fmt.Errorf("creating results dir: %w", err)
	}

	// Route each benchmark into one or more topic files.
	type topic struct {
		file   string
		filter func(BenchmarkResult) bool
	}
	topics := []topic{
		{"results/build.json", func(r BenchmarkResult) bool {
			return strings.Contains(r.Name, "Build")
		}},
		{"results/find.json", func(r BenchmarkResult) bool {
			return strings.Contains(r.Name, "Find") &&
				!strings.Contains(r.Name, "Overlapping") &&
				!strings.Contains(r.Name, "Iter_Pool")
		}},
		{"results/overlapping.json", func(r BenchmarkResult) bool {
			return strings.Contains(r.Name, "Overlapping")
		}},
		{"results/replace.json", func(r BenchmarkResult) bool {
			return strings.Contains(r.Name, "Replace")
		}},
		{"results/matchkind.json", func(r BenchmarkResult) bool {
			return strings.Contains(r.Name, "MatchKind")
		}},
		{"results/scaling.json", func(r BenchmarkResult) bool {
			return strings.Contains(r.Name, "Scaling")
		}},
		// Cross-cutting views (a benchmark can appear in multiple files).
		{"results/throughput.json", func(r BenchmarkResult) bool {
			return r.MbPerS != nil
		}},
		{"results/memory.json", func(r BenchmarkResult) bool {
			return r.AllocsPerOp > 0 || r.BytesPerOp > 0
		}},
	}

	for _, t := range topics {
		var matched []BenchmarkResult
		for _, r := range results {
			if t.filter(r) {
				matched = append(matched, r)
			}
		}
		if len(matched) == 0 {
			continue
		}
		topicRun := currentRun
		topicRun.Benchmarks = matched
		if err := appendToFile(t.file, topicRun); err != nil {
			return fmt.Errorf("writing %s: %w", t.file, err)
		}
		fmt.Printf("  → %s (%d benchmarks)\n", t.file, len(matched))
	}

	fmt.Printf("bench2json: saved %d benchmarks (run %s, commit %s)\n",
		len(results), currentRun.RunID, labelOrNA(currentRun.Commit))
	return nil
}

// ─── gen-history: results/overlapping.json → scripts/history.json ───────────

// overlapSeries defines the ordered set of series we track in the history.
var overlapSeries = []string{"NFA_Small", "DFA_Small", "NFA_Medium", "DFA_Medium"}

// benchToSeries maps a BenchmarkFindOverlapping_* name to a series label.
// Returns "" if the name doesn't match any known series.
func benchToSeries(name string) string {
	for _, backend := range []string{"NFA", "DFA"} {
		for _, size := range []string{"Small", "Medium"} {
			if strings.Contains(name, backend) && strings.Contains(name, size) {
				return backend + "_" + size
			}
		}
	}
	return ""
}

// runLabel returns a short X-axis label for a captured run.
// Prefers the git commit SHA; falls back to the date portion of the run ID.
func runLabel(r Run) string {
	if r.Commit != "" {
		return r.Commit
	}
	if len(r.RunID) >= 8 {
		return r.RunID[:8] // "20060102"
	}
	return r.RunID
}

// genHistory reads results/overlapping.json and writes scripts/history.json.
func genHistory() error {
	const src = "results/overlapping.json"
	const dst = "scripts/history.json"

	data, err := os.ReadFile(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("'%s' not found — run 'make bench' first", src)
		}
		return fmt.Errorf("reading %s: %w", src, err)
	}

	var runs []Run
	if err := json.Unmarshal(data, &runs); err != nil {
		return fmt.Errorf("parsing %s: %w", src, err)
	}
	if len(runs) == 0 {
		return fmt.Errorf("no runs in %s — run 'make bench' first", src)
	}

	histRuns := make([]HistoryRun, 0, len(runs))
	for _, r := range runs {
		hr := HistoryRun{
			Label:       runLabel(r),
			Commit:      r.Commit,
			Timestamp:   r.Timestamp,
			RunID:       r.RunID,
			NsPerOp:     make(map[string]float64),
			BytesPerOp:  make(map[string]float64),
			AllocsPerOp: make(map[string]float64),
		}
		for _, bm := range r.Benchmarks {
			s := benchToSeries(bm.Name)
			if s == "" {
				continue
			}
			hr.NsPerOp[s] = bm.NsPerOp
			hr.BytesPerOp[s] = float64(bm.BytesPerOp)
			hr.AllocsPerOp[s] = float64(bm.AllocsPerOp)
		}
		histRuns = append(histRuns, hr)
	}

	hist := History{
		Topic:  "overlapping",
		Title:  "FindOverlappingIter",
		Series: overlapSeries,
		Runs:   histRuns,
	}

	out, err := json.MarshalIndent(hist, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, out, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", dst, err)
	}

	fmt.Printf("bench2json: generated %s  (%d runs, series: %s)\n",
		dst, len(histRuns), strings.Join(overlapSeries, " | "))
	return nil
}

// ─── Shared helpers ───────────────────────────────────────────────────────────

// clearHistory reads scripts/history.json, preserves its schema (topic/title/series),
// and writes it back with an empty runs array.
func clearHistory() error {
	const dst = "scripts/history.json"

	data, err := os.ReadFile(dst)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("'%s' not found — nothing to clear", dst)
		}
		return fmt.Errorf("reading %s: %w", dst, err)
	}

	var hist History
	if err := json.Unmarshal(data, &hist); err != nil {
		return fmt.Errorf("parsing %s: %w", dst, err)
	}

	cleared := len(hist.Runs)
	hist.Runs = []HistoryRun{}

	out, err := json.MarshalIndent(hist, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, out, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", dst, err)
	}

	fmt.Printf("bench2json: cleared %d run(s) from %s\n", cleared, dst)
	return nil
}

// parseBenchLine parses one line of `go test -bench` output.
// Format: BenchmarkName-PROCS   N   ns/op [MB/s] [B/op] [allocs/op]
func parseBenchLine(line string) (BenchmarkResult, error) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return BenchmarkResult{}, fmt.Errorf("too few fields: %q", line)
	}

	nameField := fields[0]
	name := nameField
	procs := runtime.GOMAXPROCS(0)
	if idx := strings.LastIndexByte(nameField, '-'); idx >= 0 {
		if p, err := strconv.Atoi(nameField[idx+1:]); err == nil {
			name = nameField[:idx]
			procs = p
		}
	}

	n, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return BenchmarkResult{}, fmt.Errorf("parsing N: %w", err)
	}

	r := BenchmarkResult{Name: name, Procs: procs, N: int(n)}

	for i := 2; i+1 < len(fields); i++ {
		val, label := fields[i], fields[i+1]
		switch label {
		case "ns/op":
			r.NsPerOp, _ = strconv.ParseFloat(val, 64)
			i++
		case "MB/s":
			mb, err := strconv.ParseFloat(val, 64)
			if err == nil {
				r.MbPerS = &mb
			}
			i++
		case "B/op":
			r.BytesPerOp, _ = strconv.ParseInt(val, 10, 64)
			i++
		case "allocs/op":
			r.AllocsPerOp, _ = strconv.ParseInt(val, 10, 64)
			i++
		}
	}
	return r, nil
}

// appendToFile reads an existing JSON array from path, appends newRun, and
// writes it back atomically.  Creates the file if it does not exist.
func appendToFile(path string, newRun Run) error {
	var runs []Run

	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &runs); err != nil {
			return fmt.Errorf("parsing existing JSON in %s: %w", path, err)
		}
	}

	runs = append(runs, newRun)

	out, err := json.MarshalIndent(runs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0644)
}

// gitShortSHA returns the current HEAD short commit SHA, or "" on failure.
func gitShortSHA() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func labelOrNA(s string) string {
	if s == "" {
		return "n/a"
	}
	return s
}

// ─── --view ───────────────────────────────────────────────────────────────────

func viewLastRuns() {
	files := []string{
		"results/build.json",
		"results/find.json",
		"results/overlapping.json",
		"results/replace.json",
		"results/matchkind.json",
		"results/scaling.json",
		"results/throughput.json",
		"results/memory.json",
	}

	found := false
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var runs []Run
		if err := json.Unmarshal(data, &runs); err != nil || len(runs) == 0 {
			continue
		}
		found = true
		last := runs[len(runs)-1]
		fmt.Printf("\n=== %s ===\n", f)
		fmt.Printf("  Run: %s | commit: %s | %s | %s/%s | %s\n",
			last.RunID, labelOrNA(last.Commit), last.GoVersion, last.OS, last.Arch, last.CPU)
		fmt.Printf("  %-58s %12s %10s %10s %12s\n",
			"Benchmark", "ns/op", "MB/s", "B/op", "allocs/op")
		fmt.Printf("  %s\n", strings.Repeat("-", 106))
		for _, bm := range last.Benchmarks {
			mbStr := "-"
			if bm.MbPerS != nil {
				mbStr = fmt.Sprintf("%.1f", *bm.MbPerS)
			}
			fmt.Printf("  %-58s %12.0f %10s %10d %12d\n",
				bm.Name, bm.NsPerOp, mbStr, bm.BytesPerOp, bm.AllocsPerOp)
		}
	}

	if !found {
		fmt.Println("No benchmark results found. Run 'make bench' first.")
	}
}
