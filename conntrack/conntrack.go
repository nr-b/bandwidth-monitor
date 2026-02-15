package conntrack

import (
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"bandwidth-monitor/geoip"
	"bandwidth-monitor/resolver"

	ct "github.com/ti-mo/conntrack"
)

const (
	pollInterval     = 5 * time.Second
	procConntrackMax = "/proc/sys/net/netfilter/nf_conntrack_max"
	procConntrackCnt = "/proc/sys/net/netfilter/nf_conntrack_count"
)

// protoName maps IANA protocol numbers to names.
var protoName = map[uint8]string{
	1:   "ICMP",
	6:   "TCP",
	17:  "UDP",
	33:  "DCCP",
	47:  "GRE",
	58:  "ICMPv6",
	132: "SCTP",
}

// tcpStateName maps conntrack TCP state values to human-readable names.
// These match the kernel's enum tcp_conntrack (include/uapi/linux/netfilter/nf_conntrack_tcp.h).
var tcpStateName = map[uint8]string{
	0: "NONE",
	1: "SYN_SENT",
	2: "SYN_RECV",
	3: "ESTABLISHED",
	4: "FIN_WAIT",
	5: "CLOSE_WAIT",
	6: "LAST_ACK",
	7: "TIME_WAIT",
	8: "CLOSE",
	9: "SYN_SENT2",
}

// Entry represents a single conntrack entry.
type Entry struct {
	Family   string `json:"family"`   // ipv4 or ipv6
	Protocol string `json:"protocol"` // tcp, udp, icmp, etc.
	State    string `json:"state"`    // ESTABLISHED, TIME_WAIT, etc. (TCP only)
	TTL      int    `json:"ttl"`

	// Original direction
	OrigSrc     string `json:"orig_src"`
	OrigDst     string `json:"orig_dst"`
	OrigSPort   string `json:"orig_sport,omitempty"`
	OrigDPort   string `json:"orig_dport,omitempty"`
	OrigSrcHost string `json:"orig_src_host,omitempty"`
	OrigSrcGeo  string `json:"orig_src_geo,omitempty"`
	OrigSrcASN  string `json:"orig_src_asn,omitempty"`
	OrigDstHost string `json:"orig_dst_host,omitempty"`
	OrigDstGeo  string `json:"orig_dst_geo,omitempty"`
	OrigDstASN  string `json:"orig_dst_asn,omitempty"`

	// Reply direction
	ReplSrc   string `json:"repl_src"`
	ReplDst   string `json:"repl_dst"`
	ReplSPort string `json:"repl_sport,omitempty"`
	ReplDPort string `json:"repl_dport,omitempty"`

	NATType string `json:"nat_type"` // snat, dnat, both, none
	Assured bool   `json:"assured"`
	Bytes   uint64 `json:"bytes,omitempty"`
	Packets uint64 `json:"packets,omitempty"`
}

// HostStat aggregates connections per host.
type HostStat struct {
	IP          string `json:"ip"`
	Hostname    string `json:"hostname,omitempty"`
	Connections int    `json:"connections"`
	NATType     string `json:"nat_type,omitempty"`
	Country     string `json:"country,omitempty"`
	CountryName string `json:"country_name,omitempty"`
	ASN         uint   `json:"asn,omitempty"`
	ASOrg       string `json:"as_org,omitempty"`
}

// Summary holds aggregated conntrack data.
type Summary struct {
	Total    int     `json:"total"`
	Max      int     `json:"max"`
	IPv4     int     `json:"ipv4"`
	IPv6     int     `json:"ipv6"`
	UsagePct float64 `json:"usage_pct"`

	Protocols map[string]int `json:"protocols"`
	States    map[string]int `json:"states"`
	NATTypes  map[string]int `json:"nat_types"`

	TopLANClients         []HostStat `json:"top_lan_clients"`
	TopRemoteDestinations []HostStat `json:"top_remote_destinations"`

	IPv4Entries []Entry `json:"ipv4_entries"`
	IPv6Entries []Entry `json:"ipv6_entries"`

	Timestamp int64 `json:"timestamp"`
}

// Tracker periodically queries conntrack via netlink and provides a summary.
type Tracker struct {
	mu          sync.RWMutex
	summary     *Summary
	localNets   []*net.IPNet
	available   bool
	sockBufSize int // netlink socket receive buffer size
	errCount    int // consecutive dump errors (for log rate-limiting)
	stopCh      chan struct{}
	geoDB       *geoip.DB
	dns         *resolver.Resolver
	conn        *ct.Conn // persistent netlink connection
}

// Default and maximum netlink socket buffer sizes.
const (
	defaultSockBuf = 2 * 1024 * 1024  // 2 MB — enough for ~10k flows
	maxSockBuf     = 16 * 1024 * 1024 // 16 MB — for very large tables
)

// New creates a new conntrack Tracker.
// localNets defines which IPs are considered local/LAN (used to split
// top sources vs top destinations into LAN clients vs remote hosts).
func New(localNets []*net.IPNet, geoDB *geoip.DB, dns *resolver.Resolver) *Tracker {
	return &Tracker{
		localNets:   localNets,
		sockBufSize: defaultSockBuf,
		stopCh:      make(chan struct{}),
		geoDB:       geoDB,
		dns:         dns,
	}
}

// Run opens a netlink connection to verify conntrack is available, then starts
// the periodic polling loop. If conntrack netlink is not available it logs once
// and returns.
func (t *Tracker) Run() {
	// Probe: try to open a conntrack netlink connection
	c, err := ct.Dial(nil)
	if err != nil {
		log.Printf("conntrack: netlink dial failed: %v", err)
		log.Println("conntrack: NAT tracking disabled — ensure nf_conntrack and nf_conntrack_netlink modules are loaded and process has CAP_NET_ADMIN")
		log.Println("conntrack: on OpenWrt: apk add kmod-nf-conntrack-netlink  (or opkg install kmod-nf-conntrack-netlink)")
		return
	}

	// Probe dump to verify the conntrack netlink subsystem actually works.
	// The socket can open even without kmod-nf-conntrack-netlink, but dump
	// will fail with EINVAL.
	if err := c.SetReadBuffer(t.sockBufSize); err != nil {
		log.Printf("conntrack: SetReadBuffer: %v", err)
	}
	_, err = c.Dump(nil)
	c.Close()
	if err != nil {
		log.Printf("conntrack: probe dump failed: %v", err)
		log.Println("conntrack: NAT tracking disabled — the nf_conntrack_netlink kernel module is likely not loaded")
		log.Println("conntrack: on OpenWrt: apk add kmod-nf-conntrack-netlink  (or opkg install kmod-nf-conntrack-netlink)")
		return
	}

	t.available = true
	log.Println("conntrack: netlink connection established")

	t.poll()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			t.poll()
		case <-t.stopCh:
			return
		}
	}
}

// Stop terminates the polling loop.
func (t *Tracker) Stop() {
	close(t.stopCh)
	if t.conn != nil {
		t.conn.Close()
		t.conn = nil
	}
}

// GetSummary returns the latest conntrack summary (nil if unavailable).
func (t *Tracker) GetSummary() *Summary {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.summary
}

func (t *Tracker) poll() {
	if !t.available {
		return
	}

	// Reuse persistent connection; re-dial if needed.
	if t.conn == nil {
		c, err := ct.Dial(nil)
		if err != nil {
			if t.errCount == 0 || t.errCount%60 == 0 {
				log.Printf("conntrack: netlink dial: %v", err)
			}
			t.errCount++
			return
		}
		if err := c.SetReadBuffer(t.sockBufSize); err != nil {
			log.Printf("conntrack: SetReadBuffer(%d): %v", t.sockBufSize, err)
		}
		t.conn = c
	}

	flows, err := t.conn.Dump(nil)
	if err != nil {
		// Close the broken connection so we re-dial next time.
		t.conn.Close()
		t.conn = nil

		// On EINVAL / buffer-related failures, try increasing the buffer size
		if t.sockBufSize < maxSockBuf {
			t.sockBufSize *= 2
			if t.sockBufSize > maxSockBuf {
				t.sockBufSize = maxSockBuf
			}
			log.Printf("conntrack: dump failed (%v), increasing buffer to %d MB", err, t.sockBufSize/(1024*1024))
		} else if t.errCount == 0 || t.errCount%60 == 0 {
			log.Printf("conntrack: dump: %v (buffer=%d MB)", err, t.sockBufSize/(1024*1024))
		}
		t.errCount++
		return
	}
	t.errCount = 0

	max := readIntFile(procConntrackMax)
	count := readIntFile(procConntrackCnt)
	if count == 0 {
		count = len(flows)
	}

	s := &Summary{
		Total:     count,
		Max:       max,
		Protocols: make(map[string]int),
		States:    make(map[string]int),
		NATTypes:  make(map[string]int),
		Timestamp: time.Now().UnixMilli(),
	}

	if max > 0 {
		s.UsagePct = float64(count) / float64(max) * 100
	}

	srcCount := make(map[string]int) // LAN clients (local sources)
	dstCount := make(map[string]int) // remote destinations (non-local destinations)

	var ipv4Entries, ipv6Entries []Entry

	for i := range flows {
		f := &flows[i]
		e := convertFlow(f)
		if e.OrigSrc == "" {
			continue
		}

		switch e.Family {
		case "ipv4":
			s.IPv4++
			ipv4Entries = append(ipv4Entries, e)
		case "ipv6":
			s.IPv6++
			ipv6Entries = append(ipv6Entries, e)
		}

		s.Protocols[e.Protocol]++
		if e.State != "" {
			s.States[e.State]++
		}
		s.NATTypes[e.NATType]++

		// Classify: local sources → LAN clients, non-local destinations → remote hosts
		if t.isLocal(e.OrigSrc) {
			srcCount[e.OrigSrc]++
		}
		if !t.isLocal(e.OrigDst) && !t.isLoopback(e.OrigDst) {
			dstCount[e.OrigDst]++
		}
	}

	s.TopLANClients = topHosts(srcCount, 20)
	s.TopRemoteDestinations = topHosts(dstCount, 20)

	// Enrich top hosts with reverse DNS and GeoIP (outside the hot path).
	t.enrichHosts(s.TopLANClients)
	t.enrichHosts(s.TopRemoteDestinations)

	const maxEntries = 200
	// Sort by bytes (most traffic first), fall back to packets, then TTL.
	entryLess := func(a, b Entry) bool {
		if a.Bytes != b.Bytes {
			return a.Bytes > b.Bytes
		}
		if a.Packets != b.Packets {
			return a.Packets > b.Packets
		}
		return a.TTL > b.TTL
	}
	sort.Slice(ipv4Entries, func(i, j int) bool { return entryLess(ipv4Entries[i], ipv4Entries[j]) })
	sort.Slice(ipv6Entries, func(i, j int) bool { return entryLess(ipv6Entries[i], ipv6Entries[j]) })
	if len(ipv4Entries) > maxEntries {
		ipv4Entries = ipv4Entries[:maxEntries]
	}
	if len(ipv6Entries) > maxEntries {
		ipv6Entries = ipv6Entries[:maxEntries]
	}

	// Enrich the capped entry lists with reverse DNS + GeoIP.
	// Uses the shared async resolver (cache-first) and GeoIP cache.
	t.enrichEntries(ipv4Entries)
	t.enrichEntries(ipv6Entries)

	s.IPv4Entries = ipv4Entries
	s.IPv6Entries = ipv6Entries

	t.mu.Lock()
	t.summary = s
	t.mu.Unlock()
}

// convertFlow transforms a ti-mo/conntrack Flow into our Entry type.
func convertFlow(f *ct.Flow) Entry {
	var e Entry

	// Family: netip.Addr uses Is4()/Is6()
	if f.TupleOrig.IP.SourceAddress.Is4() {
		e.Family = "ipv4"
	} else {
		e.Family = "ipv6"
	}

	// Protocol
	proto := f.TupleOrig.Proto.Protocol
	if name, ok := protoName[proto]; ok {
		e.Protocol = name
	} else {
		e.Protocol = fmt.Sprintf("PROTO_%d", proto)
	}

	// TCP state
	if proto == syscall.IPPROTO_TCP {
		if name, ok := tcpStateName[f.ProtoInfo.TCP.State]; ok {
			e.State = name
		} else {
			e.State = fmt.Sprintf("STATE_%d", f.ProtoInfo.TCP.State)
		}
	}

	// TTL / timeout
	e.TTL = int(f.Timeout)

	// Original tuple
	e.OrigSrc = f.TupleOrig.IP.SourceAddress.String()
	e.OrigDst = f.TupleOrig.IP.DestinationAddress.String()
	if f.TupleOrig.Proto.SourcePort != 0 {
		e.OrigSPort = strconv.Itoa(int(f.TupleOrig.Proto.SourcePort))
	}
	if f.TupleOrig.Proto.DestinationPort != 0 {
		e.OrigDPort = strconv.Itoa(int(f.TupleOrig.Proto.DestinationPort))
	}

	// Reply tuple
	e.ReplSrc = f.TupleReply.IP.SourceAddress.String()
	e.ReplDst = f.TupleReply.IP.DestinationAddress.String()
	if f.TupleReply.Proto.SourcePort != 0 {
		e.ReplSPort = strconv.Itoa(int(f.TupleReply.Proto.SourcePort))
	}
	if f.TupleReply.Proto.DestinationPort != 0 {
		e.ReplDPort = strconv.Itoa(int(f.TupleReply.Proto.DestinationPort))
	}

	// Counters (original + reply)
	e.Bytes = f.CountersOrig.Bytes + f.CountersReply.Bytes
	e.Packets = f.CountersOrig.Packets + f.CountersReply.Packets

	// Status flags
	e.Assured = f.Status.SeenReply()

	// NAT detection: compare original vs reply tuples
	e.NATType = detectNAT(e)

	return e
}

// detectNAT determines the NAT type by comparing the original and reply tuples.
func detectNAT(e Entry) string {
	snat := e.ReplDst != "" && e.ReplDst != e.OrigSrc
	dnat := e.ReplSrc != "" && e.ReplSrc != e.OrigDst

	switch {
	case snat && dnat:
		return "both"
	case snat:
		return "snat"
	case dnat:
		return "dnat"
	default:
		return "none"
	}
}

func topHosts(counts map[string]int, n int) []HostStat {
	list := make([]HostStat, 0, len(counts))
	for ip, c := range counts {
		list = append(list, HostStat{IP: ip, Connections: c})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Connections > list[j].Connections
	})
	if len(list) > n {
		list = list[:n]
	}
	return list
}

// enrichHosts adds reverse DNS hostnames and GeoIP data to a host list.
func (t *Tracker) enrichHosts(hosts []HostStat) {
	for i := range hosts {
		ip := hosts[i].IP

		// Reverse DNS via shared resolver (TTL-based cache).
		if t.dns != nil {
			hosts[i].Hostname = t.dns.LookupAddr(ip)
		}

		// GeoIP
		if t.geoDB != nil && t.geoDB.Available() {
			if geo := t.geoDB.Lookup(ip); geo != nil {
				hosts[i].Country = geo.Country
				hosts[i].CountryName = geo.CountryName
				hosts[i].ASN = geo.ASN
				hosts[i].ASOrg = geo.ASOrg
			}
		}
	}
}

// enrichEntries adds reverse DNS hostnames and GeoIP to orig_src and orig_dst.
// Uses async resolver (returns cached or raw IP) and GeoIP cache.
func (t *Tracker) enrichEntries(entries []Entry) {
	for i := range entries {
		e := &entries[i]

		// Reverse DNS for original source and destination
		if t.dns != nil {
			e.OrigSrcHost = t.dns.LookupAddrAsync(e.OrigSrc)
			if e.OrigSrcHost == e.OrigSrc {
				e.OrigSrcHost = ""
			}
			e.OrigDstHost = t.dns.LookupAddrAsync(e.OrigDst)
			if e.OrigDstHost == e.OrigDst {
				e.OrigDstHost = ""
			}
		}

		// GeoIP for original source and destination
		if t.geoDB != nil && t.geoDB.Available() {
			if geo := t.geoDB.Lookup(e.OrigSrc); geo != nil {
				if geo.Country != "" {
					e.OrigSrcGeo = geo.Country
				}
				if geo.ASOrg != "" {
					e.OrigSrcASN = fmt.Sprintf("AS%d %s", geo.ASN, geo.ASOrg)
				}
			}
			if geo := t.geoDB.Lookup(e.OrigDst); geo != nil {
				if geo.Country != "" {
					e.OrigDstGeo = geo.Country
				}
				if geo.ASOrg != "" {
					e.OrigDstASN = fmt.Sprintf("AS%d %s", geo.ASN, geo.ASOrg)
				}
			}
		}
	}
}

func readIntFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return v
}

// isLocal checks if an IP string falls within any of the configured local networks.
// If no local networks are configured, it falls back to RFC1918/ULA checks.
func (t *Tracker) isLocal(ipStr string) bool {
	addr, err := netip.ParseAddr(ipStr)
	if err != nil {
		return false
	}
	ip := addr.As16()
	netIP := net.IP(ip[:])
	if addr.Is4() {
		netIP = netIP[12:16]
	}

	if len(t.localNets) > 0 {
		for _, n := range t.localNets {
			if n.Contains(netIP) {
				return true
			}
		}
		return false
	}

	// Fallback: RFC1918 + RFC4193 (ULA)
	return addr.IsPrivate() || addr.IsLinkLocalUnicast()
}

// isLoopback checks if an IP is a loopback address.
func (t *Tracker) isLoopback(ipStr string) bool {
	addr, err := netip.ParseAddr(ipStr)
	if err != nil {
		return false
	}
	return addr.IsLoopback()
}
