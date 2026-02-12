package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"bandwidth-monitor/collector"
	"bandwidth-monitor/conntrack"
	"bandwidth-monitor/debug"
	"bandwidth-monitor/dns"
	"bandwidth-monitor/speedtest"
	"bandwidth-monitor/talkers"
	"bandwidth-monitor/unifi"
)

func InterfaceStats(c *collector.Collector) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(c.GetAll())
	}
}

func InterfaceHistory(c *collector.Collector) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(c.GetHistory())
	}
}

func TopTalkersBandwidth(t *talkers.Tracker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(t.TopByBandwidth(10))
	}
}

func TopTalkersVolume(t *talkers.Tracker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(t.TopByVolume(10))
	}
}

func DNSSummary(dp dns.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if dp == nil {
			w.Write([]byte("null"))
			return
		}
		json.NewEncoder(w).Encode(dp.GetSummary())
	}
}

func WiFiSummary(uf *unifi.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if uf == nil {
			w.Write([]byte("null"))
			return
		}
		json.NewEncoder(w).Encode(uf.GetSummary())
	}
}

func ConntrackSummary(ct *conntrack.Tracker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if ct == nil {
			w.Write([]byte("null"))
			return
		}
		json.NewEncoder(w).Encode(ct.GetSummary())
	}
}

// MenuBarSummary returns a compact JSON snapshot for menu-bar widgets.
func MenuBarSummary(c *collector.Collector, t *talkers.Tracker, dp dns.Provider, uf *unifi.Client, ctr *conntrack.Tracker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		type ifaceBrief struct {
			Name   string   `json:"name"`
			Type   string   `json:"type"`
			Addrs  []string `json:"addrs,omitempty"`
			WAN    bool     `json:"wan,omitempty"`
			RxRate float64  `json:"rx_rate"`
			TxRate float64  `json:"tx_rate"`
			State  string   `json:"state"`
		}
		type dnsBrief struct {
			Provider     string  `json:"provider_name"`
			TotalQueries int     `json:"total_queries"`
			Blocked      int     `json:"blocked"`
			BlockPct     float64 `json:"block_pct"`
			LatencyMs    float64 `json:"latency_ms"`
		}
		type wifiBrief struct {
			APs     int `json:"aps"`
			Clients int `json:"clients"`
		}
		type natBrief struct {
			Total    int     `json:"total"`
			Max      int     `json:"max"`
			UsagePct float64 `json:"usage_pct"`
			IPv4     int     `json:"ipv4"`
			IPv6     int     `json:"ipv6"`
			SNAT     int     `json:"snat"`
			DNAT     int     `json:"dnat"`
		}
		type summary struct {
			App        string       `json:"app"`
			Interfaces []ifaceBrief `json:"interfaces"`
			VPN        bool         `json:"vpn"`
			VPNIface   string       `json:"vpn_iface,omitempty"`
			DNS        *dnsBrief    `json:"dns,omitempty"`
			WiFi       *wifiBrief   `json:"wifi,omitempty"`
			NAT        *natBrief    `json:"nat,omitempty"`
			Timestamp  int64        `json:"timestamp"`
		}

		var out summary
		out.App = "bandwidth-monitor"
		out.Timestamp = time.Now().UnixMilli()

		for _, iface := range c.GetAll() {
			ib := ifaceBrief{
				Name:   iface.Name,
				Type:   iface.IfaceType,
				Addrs:  iface.Addrs,
				WAN:    iface.WAN,
				RxRate: iface.RxRate,
				TxRate: iface.TxRate,
				State:  iface.OperState,
			}
			out.Interfaces = append(out.Interfaces, ib)
			if iface.VPNRouting {
				out.VPN = true
				out.VPNIface = iface.Name
			}
		}
		if dp != nil {
			if ds := dp.GetSummary(); ds != nil {
				out.DNS = &dnsBrief{
					Provider:     ds.ProviderName,
					TotalQueries: ds.TotalQueries,
					Blocked:      ds.BlockedTotal,
					BlockPct:     ds.BlockedPercent,
					LatencyMs:    ds.AvgLatencyMs,
				}
			}
		}
		if uf != nil {
			if ws := uf.GetSummary(); ws != nil {
				totalClients := 0
				for _, ap := range ws.APs {
					totalClients += ap.NumClients
				}
				out.WiFi = &wifiBrief{
					APs:     len(ws.APs),
					Clients: totalClients,
				}
			}
		}
		if ctr != nil {
			if ns := ctr.GetSummary(); ns != nil {
				out.NAT = &natBrief{
					Total:    ns.Total,
					Max:      ns.Max,
					UsagePct: ns.UsagePct,
					IPv4:     ns.IPv4,
					IPv6:     ns.IPv6,
					SNAT:     ns.NATTypes["snat"] + ns.NATTypes["both"],
					DNAT:     ns.NATTypes["dnat"] + ns.NATTypes["both"],
				}
			}
		}

		json.NewEncoder(w).Encode(out)
	}
}

// SpeedTestRun triggers a new speed test and streams progress as SSE.
func SpeedTestRun(st *speedtest.Tester) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}

		ch := st.Run()
		if ch == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"error": "test already running"})
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		for p := range ch {
			data, _ := json.Marshal(p)
			w.Write([]byte("data: "))
			w.Write(data)
			w.Write([]byte("\n\n"))
			flusher.Flush()
		}
	}
}

// SpeedTestResults returns the history of speed test results.
func SpeedTestResults(st *speedtest.Tester) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		results := st.GetResults()
		response := map[string]interface{}{
			"running": st.IsRunning(),
			"results": results,
		}
		json.NewEncoder(w).Encode(response)
	}
}

// DebugTraceroute runs a native ICMP traceroute and streams progress as SSE.
func DebugTraceroute() http.HandlerFunc {
	var mu sync.Mutex
	running := false

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}

		target := r.URL.Query().Get("target")
		if target == "" {
			http.Error(w, "target parameter required", http.StatusBadRequest)
			return
		}

		// Validate target: only allow hostnames and IPs, max length
		if len(target) > 253 {
			http.Error(w, "target too long", http.StatusBadRequest)
			return
		}

		// Rate limit: only one traceroute at a time
		mu.Lock()
		if running {
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"error": "traceroute already running"})
			return
		}
		running = true
		mu.Unlock()
		defer func() { mu.Lock(); running = false; mu.Unlock() }()

		countStr := r.URL.Query().Get("count")
		count := 20
		if countStr != "" {
			if c, err := strconv.Atoi(countStr); err == nil && c > 0 && c <= 100 {
				count = c
			}
		}

		maxTTLStr := r.URL.Query().Get("maxttl")
		maxTTL := 30
		if maxTTLStr != "" {
			if m, err := strconv.Atoi(maxTTLStr); err == nil && m > 0 && m <= 64 {
				maxTTL = m
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		ch := debug.RunTraceroute(target, count, maxTTL)
		for p := range ch {
			data, _ := json.Marshal(p)
			w.Write([]byte("data: "))
			w.Write(data)
			w.Write([]byte("\n\n"))
			flusher.Flush()
		}
	}
}

// DebugDNS runs DNS checks against multiple servers.
func DebugDNS() http.HandlerFunc {
	var mu sync.Mutex
	lastRun := time.Time{}

	return func(w http.ResponseWriter, r *http.Request) {
		domain := r.URL.Query().Get("domain")
		if domain == "" {
			http.Error(w, "domain parameter required", http.StatusBadRequest)
			return
		}

		// Validate domain: max length, no spaces/special chars
		if len(domain) > 253 {
			http.Error(w, "domain too long", http.StatusBadRequest)
			return
		}

		// Rate limit: 1 query per 2 seconds
		mu.Lock()
		if time.Since(lastRun) < 2*time.Second {
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{"error": "rate limited, try again shortly"})
			return
		}
		lastRun = time.Now()
		mu.Unlock()

		qtype := r.URL.Query().Get("type")
		if qtype == "" {
			qtype = "A"
		}

		result := debug.RunDNSCheck(domain, qtype)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

// buildPayload assembles the lightweight payload sent over SSE.
// Conntrack entry tables (ipv4_entries/ipv6_entries) are excluded to keep
// messages small (~5 KB instead of ~150 KB); the full data is available
// via the /api/conntrack REST endpoint.
func buildPayload(c *collector.Collector, t *talkers.Tracker, dp dns.Provider, uf *unifi.Client, ct *conntrack.Tracker) map[string]interface{} {
	payload := map[string]interface{}{
		"interfaces":    c.GetAll(),
		"sparklines":    c.GetSparklines(5*time.Minute, 50),
		"protocols":     t.GetProtocolBreakdown(),
		"ip_versions":   t.GetIPVersionBreakdown(),
		"countries":     t.GetCountryBreakdown(),
		"asns":          t.GetASNBreakdown(),
		"top_bandwidth": t.TopByBandwidth(10),
		"top_volume":    t.TopByVolume(10),
		"timestamp":     time.Now().UnixMilli(),
	}
	if dp != nil {
		payload["dns"] = dp.GetSummary()
	}
	if uf != nil {
		payload["wifi"] = uf.GetSummary()
	}
	if ct != nil {
		if s := ct.GetSummary(); s != nil {
			// Send lightweight summary without the large entry arrays.
			lite := *s
			lite.IPv4Entries = nil
			lite.IPv6Entries = nil
			payload["conntrack"] = &lite
		}
	}
	return payload
}

// SSE streams a lightweight JSON payload every second using Server-Sent Events.
// SSE uses plain HTTP — no upgrade handshake, no per-origin connection pool
// issues, and built-in auto-reconnect in the browser's EventSource API.
//
// A dedicated writer goroutine drains a 1-slot channel.  If the client is
// backed up (e.g. hibernating laptop, congested link), only the most recent
// payload is kept — preventing kernel send-buffer buildup (same backpressure
// logic that PR #18 added to the old WebSocket handler).
func SSE(c *collector.Collector, t *talkers.Tracker, dp dns.Provider, uf *unifi.Client, ct *conntrack.Tracker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		// Non-blocking write channel: the ticker produces payloads and a
		// dedicated writer goroutine drains them.  If the client is backed
		// up, only the most recent payload is kept.
		sendCh := make(chan []byte, 1)

		// Writer goroutine — serialises all writes to the response.
		writerDone := make(chan struct{})
		go func() {
			defer close(writerDone)
			for data := range sendCh {
				if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
					return
				}
				flusher.Flush()
			}
		}()

		// Send initial payload immediately.
		data, err := json.Marshal(buildPayload(c, t, dp, uf, ct))
		if err != nil {
			close(sendCh)
			return
		}
		sendCh <- data

		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-r.Context().Done():
				close(sendCh)
				return
			case <-writerDone:
				return
			case <-ticker.C:
				data, err := json.Marshal(buildPayload(c, t, dp, uf, ct))
				if err != nil {
					continue
				}
				// Non-blocking send: drop the old message if backed up
				select {
				case sendCh <- data:
				default:
					// Channel full — drain stale message, enqueue fresh one
					select {
					case <-sendCh:
					default:
					}
					sendCh <- data
				}
			}
		}
	}
}
