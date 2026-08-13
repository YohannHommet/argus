package sim

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

// Report is SPEC §7.2's exit report: "sessions/events/hooks/metric points
// sent, HTTP status histogram, throughput, and non-2xx bodies — so a
// 429/503 storm during load testing is legible." Every field is exported
// so runner.go's tests can assert on it directly without parsing Print's
// text output.
type Report struct {
	mu sync.Mutex

	Sessions     int
	LogEvents    int
	HookEvents   int
	MetricPoints int

	// StatusHistogram counts HTTP responses by status code. Only populated
	// for HTTPTransport runs; a FileTransport run's histogram stays empty
	// (there is no HTTP exchange to count, doc.go's Transport contract).
	StatusHistogram map[int]int

	// NonOKBodies keeps up to nonOKBodiesCap response bodies from non-2xx
	// responses, so an operator can see *why* a batch of 429s happened
	// without re-running with a packet capture.
	NonOKBodies []string

	Errors []string

	Started  time.Time
	Finished time.Time
}

// nonOKBodiesCap bounds how many non-2xx bodies Report retains, so a
// sustained outage during --mode=load cannot make the exit report itself
// consume unbounded memory.
const nonOKBodiesCap = 20

// NewReport starts a Report with its Started timestamp set to now — a real
// wall-clock read is correct here (unlike event generation): the report
// measures the run's own real-world duration/throughput, which is
// meaningful regardless of --clock-origin.
func NewReport() *Report {
	return &Report{StatusHistogram: map[int]int{}, Started: time.Now()}
}

// RecordSend folds one Transport call's SendResult into the report. kind is
// "logs"|"metrics"|"hooks", used only to pick which counter to bump — the
// caller passes the count of individual events the batch represented, not
// the batch count, so LogEvents/HookEvents/MetricPoints always mean
// "events", never "HTTP requests".
func (r *Report) RecordSend(kind string, eventCount int, res SendResult) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch kind {
	case "logs":
		r.LogEvents += eventCount
	case "metrics":
		r.MetricPoints += eventCount
	case "hooks":
		r.HookEvents += eventCount
	}

	if res.Err != nil {
		r.Errors = append(r.Errors, res.Err.Error())
		return
	}
	if res.StatusCode == 0 {
		return // FileTransport: no HTTP exchange to histogram
	}
	r.StatusHistogram[res.StatusCode]++
	if (res.StatusCode < 200 || res.StatusCode >= 300) && len(r.NonOKBodies) < nonOKBodiesCap {
		r.NonOKBodies = append(r.NonOKBodies, fmt.Sprintf("%d: %s", res.StatusCode, truncate(string(res.Body), 256)))
	}
}

// RecordSession increments the session counter by one. Called once per
// generated session regardless of Transport.
func (r *Report) RecordSession() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Sessions++
}

// Finish stamps Finished (called once, when the run loop exits).
func (r *Report) Finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Finished = time.Now()
}

// Throughput reports events/sec (logs+hooks+metric points) over the
// Started..Finished window, the number SPEC §7.2's load-mode AC checks
// against --rate.
func (r *Report) Throughput() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	elapsed := r.Finished.Sub(r.Started).Seconds()
	if elapsed <= 0 {
		return 0
	}
	return float64(r.LogEvents+r.HookEvents+r.MetricPoints) / elapsed
}

// AllOK reports whether every recorded HTTP response was 2xx and no
// transport error occurred — the condition SPEC §7's
// "--mode=demo --sessions=5 --flush-immediately exits 0 with an all-2xx
// histogram" AC checks.
func (r *Report) AllOK() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.Errors) > 0 {
		return false
	}
	for code := range r.StatusHistogram {
		if code < 200 || code >= 300 {
			return false
		}
	}
	return true
}

// Print writes the human-readable exit report to w (SPEC §7.2's
// deliverable, not decoration: "so a 429/503 storm during load testing is
// legible").
func (r *Report) Print(w io.Writer) {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, _ = fmt.Fprintf(w, "argus-sim: %d sessions, %d log events, %d hook events, %d metric points\n", // best-effort write; a failed write to this stream has no recovery action here
		r.Sessions, r.LogEvents, r.HookEvents, r.MetricPoints)

	elapsed := r.Finished.Sub(r.Started)
	var throughput float64
	if elapsed.Seconds() > 0 {
		throughput = float64(r.LogEvents+r.HookEvents+r.MetricPoints) / elapsed.Seconds()
	}
	_, _ = fmt.Fprintf(w, "argus-sim: elapsed %s, throughput %.1f events/s\n", elapsed, throughput) // best-effort write; a failed write to this stream has no recovery action here

	if len(r.StatusHistogram) > 0 {
		codes := make([]int, 0, len(r.StatusHistogram))
		for c := range r.StatusHistogram {
			codes = append(codes, c)
		}
		sort.Ints(codes)
		_, _ = fmt.Fprint(w, "argus-sim: HTTP status histogram:") // best-effort write; a failed write to this stream has no recovery action here
		for _, c := range codes {
			_, _ = fmt.Fprintf(w, " %d=%d", c, r.StatusHistogram[c]) // best-effort write; a failed write to this stream has no recovery action here
		}
		_, _ = fmt.Fprintln(w) // best-effort write; a failed write to this stream has no recovery action here
	}

	for _, b := range r.NonOKBodies {
		_, _ = fmt.Fprintf(w, "argus-sim: non-2xx: %s\n", b) // best-effort write; a failed write to this stream has no recovery action here
	}
	for _, e := range r.Errors {
		_, _ = fmt.Fprintf(w, "argus-sim: error: %s\n", e) // best-effort write; a failed write to this stream has no recovery action here
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
