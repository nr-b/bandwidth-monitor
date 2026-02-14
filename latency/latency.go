// Package latency provides continuous ICMP and HTTPS latency monitoring
// against configurable targets, storing a rolling history of RTT measurements.
// Targets with both IPv4 and IPv6 addresses are probed on both protocols.
package latency

import (
	"context"
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
	"golang.org/x/net/ipv6"
)

const (
	probeInterval = 2 * time.Second
	icmpTimeout   = 900 * time.Millisecond
	httpsTimeout  = 3 * time.Second
	maxHistory    = 450 // 15 minutes at 2s interval
)

// DefaultTargets are used when no LATENCY_TARGETS env var is set.
// All defaults are European-operated, privacy-friendly services.
var DefaultTargets = []string{
	"anycast01.ffmuc.net",
	"anycast02.ffmuc.net",
	"dns.quad9.net",
	"dns3.digitalcourage.de",
}

// Point is a single latency measurement.
type Point struct {
	Timestamp int64   `json:"t"`
	RTT       float64 `json:"rtt"` // milliseconds, -1 = timeout/loss
}

// TargetStatus holds the current state of a monitored target.
type TargetStatus struct {
	Name    string  `json:"name"`
	IPv4    string  `json:"ipv4,omitempty"`
	IPv6    string  `json:"ipv6,omitempty"`
	Alive   bool    `json:"alive"`
	RTT     float64 `json:"rtt_ms"`
	AvgRTT  float64 `json:"avg_rtt_ms"`
	MinRTT  float64 `json:"min_rtt_ms"`
	MaxRTT  float64 `json:"max_rtt_ms"`
	Jitter  float64 `json:"jitter_ms"`
	LossPct float64 `json:"loss_pct"`
	ICMPv4  []Point `json:"icmp_v4,omitempty"`
	ICMPv6  []Point `json:"icmp_v6,omitempty"`
	HTTPSv4 []Point `json:"https_v4,omitempty"`
	HTTPSv6 []Point `json:"https_v6,omitempty"`
	// Legacy fields (preferred stack) for backward compat
	ICMP  []Point `json:"icmp"`
	HTTPS []Point `json:"https"`
}

// Monitor continuously probes a set of targets via ICMP and HTTPS.
type Monitor struct {
	targets []resolvedTarget
	mu      sync.RWMutex
	state   map[string]*targetState
	stopCh  chan struct{}
}

type resolvedTarget struct {
	name string
	ipv4 net.IP // nil if no v4
	ipv6 net.IP // nil if no v6
}

type targetState struct {
	icmpV4Hist  []Point
	icmpV6Hist  []Point
	httpsV4Hist []Point
	httpsV6Hist []Point
	httpC4      *http.Client // direct-IP HTTPS client for IPv4 (nil if no v4)
	httpC6      *http.Client // direct-IP HTTPS client for IPv6 (nil if no v6)
}

// newDirectHTTPSClient returns an HTTP client that connects directly to the
// given IP address, bypassing DNS resolution entirely.  The TLS ServerName
// is set to hostname so certificate validation works correctly.
func newDirectHTTPSClient(ip net.IP, hostname string) *http.Client {
	network := "tcp4"
	ipStr := ip.String()
	if ip.To4() == nil {
		network = "tcp6"
		ipStr = "[" + ipStr + "]"
	}
	dialer := &net.Dialer{Timeout: httpsTimeout}
	return &http.Client{
		Timeout: httpsTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				ServerName: hostname,
			},
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				// Replace the hostname in addr with the resolved IP.
				_, port, _ := net.SplitHostPort(addr)
				return dialer.DialContext(ctx, network, ipStr+":"+port)
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// New creates a latency monitor. Pass nil or empty targets to use defaults.
func New(targets []string) *Monitor {
	if len(targets) == 0 {
		targets = DefaultTargets
	}

	var resolved []resolvedTarget
	for _, t := range targets {
		rt := resolvedTarget{name: t}
		ips, err := net.LookupIP(t)
		if err != nil || len(ips) == 0 {
			ip := net.ParseIP(t)
			if ip == nil {
				fmt.Fprintf(os.Stderr, "latency: cannot resolve %q, skipping\n", t)
				continue
			}
			if ip.To4() != nil {
				rt.ipv4 = ip.To4()
			} else {
				rt.ipv6 = ip
			}
			resolved = append(resolved, rt)
			continue
		}
		for _, ip := range ips {
			if ip.To4() != nil && rt.ipv4 == nil {
				rt.ipv4 = ip.To4()
			} else if ip.To4() == nil && rt.ipv6 == nil {
				rt.ipv6 = ip
			}
		}
		if rt.ipv4 != nil || rt.ipv6 != nil {
			resolved = append(resolved, rt)
		}
	}

	m := &Monitor{
		targets: resolved,
		state:   make(map[string]*targetState, len(resolved)),
		stopCh:  make(chan struct{}),
	}
	for _, t := range resolved {
		st := &targetState{}
		if t.ipv4 != nil {
			st.httpC4 = newDirectHTTPSClient(t.ipv4, t.name)
		}
		if t.ipv6 != nil {
			st.httpC6 = newDirectHTTPSClient(t.ipv6, t.name)
		}
		m.state[t.name] = st
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
		v4s, v6s := "—", "—"
		if t.ipv4 != nil {
			v4s = t.ipv4.String()
		}
		if t.ipv6 != nil {
			v6s = t.ipv6.String()
		}
		log.Printf("  %s (v4=%s, v6=%s)", t.name, v4s, v6s)
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
		ts := TargetStatus{Name: t.name}
		if t.ipv4 != nil {
			ts.IPv4 = t.ipv4.String()
		}
		if t.ipv6 != nil {
			ts.IPv6 = t.ipv6.String()
		}

		// Primary stats from ICMPv4, fallback to ICMPv6
		primary := st.icmpV4Hist
		if len(primary) == 0 {
			primary = st.icmpV6Hist
		}
		if len(primary) > 0 {
			last := primary[len(primary)-1]
			ts.Alive = last.RTT >= 0
			ts.RTT = last.RTT
			ts.AvgRTT, ts.MinRTT, ts.MaxRTT, ts.Jitter = computeStats(primary)
			var lost int
			for _, p := range primary {
				if p.RTT < 0 {
					lost++
				}
			}
			ts.LossPct = float64(lost) / float64(len(primary)) * 100
		}

		ts.ICMPv4 = copyPoints(st.icmpV4Hist)
		ts.ICMPv6 = copyPoints(st.icmpV6Hist)
		ts.HTTPSv4 = copyPoints(st.httpsV4Hist)
		ts.HTTPSv6 = copyPoints(st.httpsV6Hist)

		// Legacy: prefer v4 if available
		if len(ts.ICMPv4) > 0 {
			ts.ICMP = ts.ICMPv4
		} else {
			ts.ICMP = ts.ICMPv6
		}
		if len(ts.HTTPSv4) > 0 {
			ts.HTTPS = ts.HTTPSv4
		} else {
			ts.HTTPS = ts.HTTPSv6
		}

		result = append(result, ts)
	}
	return result
}

func copyPoints(s []Point) []Point {
	if len(s) == 0 {
		return nil
	}
	c := make([]Point, len(s))
	copy(c, s)
	return c
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

	// Open ICMP sockets for both v4 and v6
	conn4, err4 := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err4 != nil && !icmpV4Logged {
		fmt.Fprintf(os.Stderr, "latency: ICMPv4 unavailable (need root/CAP_NET_RAW): %v\n", err4)
		icmpV4Logged = true
	}
	if conn4 != nil {
		defer conn4.Close()
	}

	conn6, err6 := icmp.ListenPacket("ip6:ipv6-icmp", "::")
	if err6 != nil && !icmpV6Logged {
		fmt.Fprintf(os.Stderr, "latency: ICMPv6 unavailable: %v\n", err6)
		icmpV6Logged = true
	}
	if conn6 != nil {
		defer conn6.Close()
	}

	for i, t := range m.targets {
		var icmpV4RTT, icmpV6RTT float64 = -1, -1
		var httpsV4RTT, httpsV6RTT float64 = -1, -1

		// ICMPv4
		if conn4 != nil && t.ipv4 != nil {
			icmpV4RTT = pingOneV4(conn4, t.ipv4, i)
		}
		// ICMPv6
		if conn6 != nil && t.ipv6 != nil {
			icmpV6RTT = pingOneV6(conn6, t.ipv6, i)
		}
		// HTTPSv4 -- connect directly to the resolved IPv4 address (no DNS)
		if t.ipv4 != nil {
			httpsV4RTT = m.probeHTTPS(m.state[t.name].httpC4, t.name)
		}
		// HTTPSv6 -- connect directly to the resolved IPv6 address (no DNS)
		if t.ipv6 != nil {
			httpsV6RTT = m.probeHTTPS(m.state[t.name].httpC6, t.name)
		}

		m.mu.Lock()
		st := m.state[t.name]
		if t.ipv4 != nil {
			st.icmpV4Hist = appendAndTrim(st.icmpV4Hist, Point{Timestamp: ts, RTT: icmpV4RTT})
			st.httpsV4Hist = appendAndTrim(st.httpsV4Hist, Point{Timestamp: ts, RTT: httpsV4RTT})
		}
		if t.ipv6 != nil {
			st.icmpV6Hist = appendAndTrim(st.icmpV6Hist, Point{Timestamp: ts, RTT: icmpV6RTT})
			st.httpsV6Hist = appendAndTrim(st.httpsV6Hist, Point{Timestamp: ts, RTT: httpsV6RTT})
		}
		m.mu.Unlock()
	}
}

func appendAndTrim(s []Point, p Point) []Point {
	s = append(s, p)
	if len(s) > maxHistory {
		trimmed := make([]Point, maxHistory)
		copy(trimmed, s[len(s)-maxHistory:])
		return trimmed
	}
	return s
}

var (
	icmpV4Logged bool
	icmpV6Logged bool
)

func pingOneV4(conn *icmp.PacketConn, dest net.IP, seq int) float64 {
	id := uint16(os.Getpid() & 0xFFFF)
	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho, Code: 0,
		Body: &icmp.Echo{ID: int(id), Seq: seq, Data: []byte("bwmon-lat")},
	}
	wb, err := msg.Marshal(nil)
	if err != nil {
		return -1
	}
	dst := &net.IPAddr{IP: dest}
	conn.SetDeadline(time.Now().Add(icmpTimeout))
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

func pingOneV6(conn *icmp.PacketConn, dest net.IP, seq int) float64 {
	id := uint16(os.Getpid() & 0xFFFF)
	msg := icmp.Message{
		Type: ipv6.ICMPTypeEchoRequest, Code: 0,
		Body: &icmp.Echo{ID: int(id), Seq: seq, Data: []byte("bwmon-lat")},
	}
	wb, err := msg.Marshal(nil)
	if err != nil {
		return -1
	}
	dst := &net.IPAddr{IP: dest}
	conn.SetDeadline(time.Now().Add(icmpTimeout))
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
		rm, err := icmp.ParseMessage(58, rb[:n])
		if err != nil {
			continue
		}
		if rm.Type == ipv6.ICMPTypeEchoReply {
			if echo, ok := rm.Body.(*icmp.Echo); ok {
				if uint16(echo.ID) == id {
					return rtt
				}
			}
		}
	}
}

func (m *Monitor) probeHTTPS(client *http.Client, hostname string) float64 {
	url := "https://" + hostname
	start := time.Now()
	resp, err := client.Get(url)
	if err != nil {
		return -1
	}
	resp.Body.Close()
	return float64(time.Since(start).Microseconds()) / 1000.0
}
