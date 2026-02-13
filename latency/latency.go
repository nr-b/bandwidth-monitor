// Package latency provides continuous ICMP and HTTPS latency monitoring
// against configurable targets, storing a rolling history of RTT measurements.
package latency

import (
	"crypto/tls"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

const (
	probeInterval = 2 * time.Second
	icmpTimeout   = 900 * time.Millisecond
	httpsTimeout  = 3 * time.Second
	maxHistory    = 300 // 5 minutes at 2s interval
)

// DefaultTargets are used when no LATENCY_TARGETS env var is set.
var DefaultTargets = []string{
	"anycast01.ffmuc.net",
	"anycast02.ffmuc.net",
	"github.com",
}

// Point is a single latency measurement.
type Point struct {
	Timestamp int64   `json:"t"`
	RTT       float64 `json:"rtt"` // milliseconds, -1 = timeout/loss
}

// TargetStatus holds the current state of a monitored target.
type TargetStatus struct {
	Name    string  `json:"name"`
	IP      string  `json:"ip"`
	Alive   bool    `json:"alive"`
	RTT     float64 `json:"rtt_ms"`
	AvgRTT  float64 `json:"avg_rtt_ms"`
	MinRTT  float64 `json:"min_rtt_ms"`
	MaxRTT  float64 `json:"max_rtt_ms"`
	Jitter  float64 `json:"jitter_ms"`
	LossPct float64 `json:"loss_pct"`
	ICMP    []Point `json:"icmp"`
	HTTPS   []Point `json:"https"`
}

// Monitor continuously probes a set of targets via ICMP and HTTPS.
type Monitor struct {
	targets []resolvedTarget
	mu      sync.RWMutex
	state   map[string]*targetState
	httpC   *http.Client
	stopCh  chan struct{}
}

type resolvedTarget struct {
	name string
	ip   net.IP
}

type targetState struct {
	icmpHist  []Point
	httpsHist []Point
}

// New creates a latency monitor. Pass nil or empty targets to use defaults.
func New(targets []string) *Monitor {
	if len(targets) == 0 {
		targets = DefaultTargets
	}

	var resolved []resolvedTarget
	for _, t := range targets {
		ips, err := net.LookupIP(t)
		if err != nil || len(ips) == 0 {
			ip := net.ParseIP(t)
			if ip == nil {
				fmt.Fprintf(os.Stderr, "latency: cannot resolve %q, skipping\n", t)
				continue
			}
			resolved = append(resolved, resolvedTarget{name: t, ip: ip})
			continue
		}
		var chosen net.IP
		for _, ip := range ips {
			if ip.To4() != nil {
				chosen = ip.To4()
				break
			}
		}
		if chosen == nil {
			chosen = ips[0]
		}
		resolved = append(resolved, resolvedTarget{name: t, ip: chosen})
	}

	m := &Monitor{
		targets: resolved,
		state:   make(map[string]*targetState, len(resolved)),
		httpC: &http.Client{
			Timeout: httpsTimeout,
			Transport: &http.Transport{
				TLSClientConfig:   &tls.Config{InsecureSkipVerify: false},
				DisableKeepAlives: true,
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		stopCh: make(chan struct{}),
	}
	for _, t := range resolved {
		m.state[t.name] = &targetState{}
	}
	return m
}

// Run starts the probe loop. Call in a goroutine.
func (m *Monitor) Run() {
	if len(m.targets) == 0 {
		return
	}
	log.Printf("latency: monitoring %d target(s)", len(m.targets))
	for _, t := range m.targets {
		log.Printf("  %s (%s)", t.name, t.ip)
	}

	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()
	m.probeAll()
	for {
		select {
		case <-ticker.C:
			m.probeAll()
		case <-m.stopCh:
			return
		}
	}
}

// Stop terminates the probe loop.
func (m *Monitor) Stop() {
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
}

// GetStatus returns the current status of all targets.
func (m *Monitor) GetStatus() []TargetStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]TargetStatus, 0, len(m.targets))
	for _, t := range m.targets {
		st := m.state[t.name]
		ts := TargetStatus{
			Name: t.name,
			IP:   t.ip.String(),
		}

		// Use ICMP history for alive/rtt/stats
		if len(st.icmpHist) > 0 {
			last := st.icmpHist[len(st.icmpHist)-1]
			ts.Alive = last.RTT >= 0
			ts.RTT = last.RTT
			ts.AvgRTT, ts.MinRTT, ts.MaxRTT, ts.Jitter = computeStats(st.icmpHist)
		}

		// Compute loss from ICMP history window (not lifetime counters)
		if len(st.icmpHist) > 0 {
			var lost int
			for _, p := range st.icmpHist {
				if p.RTT < 0 {
					lost++
				}
			}
			ts.LossPct = float64(lost) / float64(len(st.icmpHist)) * 100
		}

		ts.ICMP = make([]Point, len(st.icmpHist))
		copy(ts.ICMP, st.icmpHist)
		ts.HTTPS = make([]Point, len(st.httpsHist))
		copy(ts.HTTPS, st.httpsHist)

		result = append(result, ts)
	}
	return result
}

func computeStats(pts []Point) (avg, min, max, jitter float64) {
	min = math.MaxFloat64
	var sum float64
	var count int
	var prev float64
	var jitterSum float64
	hasPrev := false

	for _, p := range pts {
		if p.RTT < 0 {
			hasPrev = false
			continue
		}
		sum += p.RTT
		count++
		if p.RTT < min {
			min = p.RTT
		}
		if p.RTT > max {
			max = p.RTT
		}
		if hasPrev {
			jitterSum += math.Abs(p.RTT - prev)
		}
		prev = p.RTT
		hasPrev = true
	}
	if count == 0 {
		return 0, 0, 0, 0
	}
	avg = sum / float64(count)
	if count > 1 {
		jitter = jitterSum / float64(count-1)
	}
	return
}

func (m *Monitor) probeAll() {
	now := time.Now()
	ts := now.UnixMilli()

	// ICMP: open one socket for all targets
	conn, icmpErr := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if icmpErr != nil && !icmpLogged {
		fmt.Fprintf(os.Stderr, "latency: ICMP unavailable (need root/CAP_NET_RAW): %v\n", icmpErr)
		icmpLogged = true
	}
	if conn != nil {
		defer conn.Close()
	}

	for i, t := range m.targets {
		var icmpRTT float64 = -1
		var httpsRTT float64 = -1

		// ICMP probe
		if conn != nil && t.ip.To4() != nil {
			icmpRTT = pingOne(conn, t.ip, i, now)
		}

		// HTTPS probe
		httpsRTT = m.probeHTTPS(t.name)

		m.mu.Lock()
		st := m.state[t.name]

		st.icmpHist = appendAndTrim(st.icmpHist, Point{Timestamp: ts, RTT: icmpRTT})
		st.httpsHist = appendAndTrim(st.httpsHist, Point{Timestamp: ts, RTT: httpsRTT})

		m.mu.Unlock()
	}
}

// appendAndTrim appends a point and caps the slice at maxHistory.
// When trimming, it copies to a new slice to release the old backing array.
func appendAndTrim(s []Point, p Point) []Point {
	s = append(s, p)
	if len(s) > maxHistory {
		trimmed := make([]Point, maxHistory)
		copy(trimmed, s[len(s)-maxHistory:])
		return trimmed
	}
	return s
}

var icmpLogged bool

func pingOne(conn *icmp.PacketConn, dest net.IP, seq int, now time.Time) float64 {
	id := uint16(os.Getpid() & 0xFFFF)
	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{
			ID:   int(id),
			Seq:  seq,
			Data: []byte("bwmon-lat"),
		},
	}
	wb, err := msg.Marshal(nil)
	if err != nil {
		return -1
	}

	dst := &net.IPAddr{IP: dest}
	conn.SetDeadline(now.Add(icmpTimeout))
	start := time.Now()
	if _, err := conn.WriteTo(wb, dst); err != nil {
		return -1
	}

	rb := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFrom(rb)
		if err != nil {
			return -1
		}
		rtt := float64(time.Since(start).Microseconds()) / 1000.0
		rm, err := icmp.ParseMessage(1, rb[:n])
		if err != nil {
			continue
		}
		if rm.Type == ipv4.ICMPTypeEchoReply {
			if echo, ok := rm.Body.(*icmp.Echo); ok {
				if uint16(echo.ID) == id {
					return rtt
				}
			}
		}
	}
}

func (m *Monitor) probeHTTPS(hostname string) float64 {
	url := "https://" + hostname
	start := time.Now()
	resp, err := m.httpC.Get(url)
	if err != nil {
		return -1
	}
	resp.Body.Close()
	return float64(time.Since(start).Microseconds()) / 1000.0
}
