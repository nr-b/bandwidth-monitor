package speedtest

import (
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Result holds the outcome of a single speed test run.
type Result struct {
	Server       string  `json:"server"`
	DownloadMbps float64 `json:"download_mbps"`
	UploadMbps   float64 `json:"upload_mbps"`
	PingMs       float64 `json:"ping_ms"`
	JitterMs     float64 `json:"jitter_ms"`
	Timestamp    int64   `json:"timestamp"`
}

// Progress is sent over SSE while a test is running.
type Progress struct {
	Phase   string  `json:"phase"`
	Percent float64 `json:"percent"`
	Value   float64 `json:"value"`
	Result  *Result `json:"result,omitempty"`
}

// Tester manages speed tests against a configured server.
type Tester struct {
	server string

	mu       sync.Mutex
	running  bool
	results  []Result
	progress chan Progress
}

// New creates a Tester for the given server URL (no trailing slash).
func New(server string) *Tester {
	return &Tester{
		server:  server,
		results: make([]Result, 0),
	}
}

// IsRunning returns whether a test is currently in progress.
func (t *Tester) IsRunning() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running
}

// GetResults returns a copy of all stored results (newest first).
func (t *Tester) GetResults() []Result {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Result, len(t.results))
	copy(out, t.results)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Run starts a speed test in the background. Returns a channel that receives
// progress updates. If a test is already running, returns nil.
func (t *Tester) Run() <-chan Progress {
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return nil
	}
	t.running = true
	ch := make(chan Progress, 32)
	t.progress = ch
	t.mu.Unlock()

	go t.run(ch)
	return ch
}

func (t *Tester) run(ch chan<- Progress) {
	defer func() {
		t.mu.Lock()
		t.running = false
		t.progress = nil
		t.mu.Unlock()
		close(ch)
	}()

	server := t.server
	log.Printf("speedtest: starting test against %s", server)

	ch <- Progress{Phase: "ping", Percent: 0, Value: 0}
	pingMs, jitterMs, err := measurePing(server, 20)
	if err != nil {
		log.Printf("speedtest: ping failed: %v", err)
		ch <- Progress{Phase: "error", Value: 0}
		return
	}
	ch <- Progress{Phase: "ping", Percent: 100, Value: pingMs}
	log.Printf("speedtest: ping=%.1fms jitter=%.1fms", pingMs, jitterMs)

	ch <- Progress{Phase: "download", Percent: 0, Value: 0}
	dlMbps, err := measureDownload(server, ch)
	if err != nil {
		log.Printf("speedtest: download failed: %v", err)
		ch <- Progress{Phase: "error", Value: 0}
		return
	}
	log.Printf("speedtest: download=%.1f Mbps", dlMbps)

	ch <- Progress{Phase: "upload", Percent: 0, Value: 0}
	ulMbps, err := measureUpload(server, ch)
	if err != nil {
		log.Printf("speedtest: upload failed: %v", err)
		ch <- Progress{Phase: "error", Value: 0}
		return
	}
	log.Printf("speedtest: upload=%.1f Mbps", ulMbps)

	result := Result{
		Server:       server,
		DownloadMbps: dlMbps,
		UploadMbps:   ulMbps,
		PingMs:       pingMs,
		JitterMs:     jitterMs,
		Timestamp:    time.Now().UnixMilli(),
	}

	t.mu.Lock()
	t.results = append(t.results, result)
	if len(t.results) > 50 {
		t.results = t.results[len(t.results)-50:]
	}
	t.mu.Unlock()

	ch <- Progress{Phase: "done", Percent: 100, Result: &result}
	log.Printf("speedtest: completed — DL %.1f Mbps, UL %.1f Mbps, Ping %.1f ms",
		dlMbps, ulMbps, pingMs)
}

func measurePing(server string, samples int) (avgMs, jitterMs float64, err error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	var pings []float64
	for i := 0; i < samples; i++ {
		start := time.Now()
		req, e := http.NewRequest("HEAD", server+"/downloading", nil)
		if e != nil {
			return 0, 0, fmt.Errorf("creating request: %w", e)
		}
		resp, e := client.Do(req)
		if e != nil {
			continue
		}
		resp.Body.Close()
		rtt := float64(time.Since(start).Microseconds()) / 1000.0
		pings = append(pings, rtt)
	}

	if len(pings) < 2 {
		return 0, 0, fmt.Errorf("not enough ping responses (%d/%d)", len(pings), samples)
	}

	sort.Float64s(pings)
	qStart := len(pings) / 4
	qEnd := len(pings) - qStart
	if qEnd <= qStart {
		qStart = 0
		qEnd = len(pings)
	}

	var sum float64
	for i := qStart; i < qEnd; i++ {
		sum += pings[i]
	}
	avgMs = sum / float64(qEnd-qStart)

	var jitterSum float64
	count := 0
	for i := 1; i < len(pings); i++ {
		diff := pings[i] - pings[i-1]
		if diff < 0 {
			diff = -diff
		}
		jitterSum += diff
		count++
	}
	if count > 0 {
		jitterMs = jitterSum / float64(count)
	}

	return avgMs, jitterMs, nil
}

func measureDownload(server string, ch chan<- Progress) (float64, error) {
	const (
		duration    = 15 * time.Second
		parallelism = 6
	)

	var totalBytes int64
	var mu sync.Mutex
	deadline := time.Now().Add(duration)

	var wg sync.WaitGroup

	for i := 0; i < parallelism; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &http.Client{
				Timeout: duration + 5*time.Second,
			}
			buf := make([]byte, 256*1024)
			for time.Now().Before(deadline) {
				resp, e := client.Get(server + "/downloading")
				if e != nil {
					return
				}
				for time.Now().Before(deadline) {
					n, e := resp.Body.Read(buf)
					if n > 0 {
						mu.Lock()
						totalBytes += int64(n)
						mu.Unlock()
					}
					if e != nil {
						break
					}
				}
				resp.Body.Close()
			}
		}()
	}

	startTime := time.Now()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

loop:
	for {
		select {
		case <-done:
			break loop
		case <-ticker.C:
			elapsed := time.Since(startTime).Seconds()
			pct := (elapsed / duration.Seconds()) * 100
			if pct > 100 {
				pct = 100
			}
			mu.Lock()
			b := totalBytes
			mu.Unlock()
			mbps := (float64(b) * 8) / (elapsed * 1e6)
			ch <- Progress{Phase: "download", Percent: pct, Value: mbps}
		}
	}

	mu.Lock()
	b := totalBytes
	mu.Unlock()

	elapsed := time.Since(startTime).Seconds()
	if elapsed == 0 || b == 0 {
		return 0, fmt.Errorf("no data downloaded")
	}

	mbps := (float64(b) * 8) / (elapsed * 1e6)
	ch <- Progress{Phase: "download", Percent: 100, Value: mbps}
	return mbps, nil
}

func measureUpload(server string, ch chan<- Progress) (float64, error) {
	const (
		duration    = 15 * time.Second
		parallelism = 6
		chunkSize   = 4 * 1024 * 1024
	)

	var totalBytes int64
	var mu sync.Mutex
	deadline := time.Now().Add(duration)

	var wg sync.WaitGroup

	for i := 0; i < parallelism; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &http.Client{
				Timeout: duration + 5*time.Second,
			}
			data := make([]byte, chunkSize)
			rand.Read(data)

			for time.Now().Before(deadline) {
				reader := &countingReader{
					data:    data,
					mu:      &mu,
					counter: &totalBytes,
				}
				req, e := http.NewRequest("POST", server+"/upload", reader)
				if e != nil {
					return
				}
				req.ContentLength = int64(chunkSize)
				req.Header.Set("Content-Type", "application/octet-stream")

				resp, e := client.Do(req)
				if e != nil {
					continue
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}

	startTime := time.Now()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

loop:
	for {
		select {
		case <-done:
			break loop
		case <-ticker.C:
			elapsed := time.Since(startTime).Seconds()
			pct := (elapsed / duration.Seconds()) * 100
			if pct > 100 {
				pct = 100
			}
			mu.Lock()
			b := totalBytes
			mu.Unlock()
			mbps := (float64(b) * 8) / (elapsed * 1e6)
			ch <- Progress{Phase: "upload", Percent: pct, Value: mbps}
		}
	}

	mu.Lock()
	b := totalBytes
	mu.Unlock()

	elapsed := time.Since(startTime).Seconds()
	if elapsed == 0 || b == 0 {
		return 0, fmt.Errorf("no data uploaded")
	}

	mbps := (float64(b) * 8) / (elapsed * 1e6)
	ch <- Progress{Phase: "upload", Percent: 100, Value: mbps}
	return mbps, nil
}

type countingReader struct {
	data    []byte
	offset  int
	mu      *sync.Mutex
	counter *int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	r.mu.Lock()
	*r.counter += int64(n)
	r.mu.Unlock()
	return n, nil
}
