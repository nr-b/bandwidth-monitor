package talkers

import (
	"log"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"bandwidth-monitor/geoip"
	"bandwidth-monitor/packets"
	"bandwidth-monitor/resolver"

	"golang.org/x/sys/unix"
)

const (
	snapshotLen       int32         = 128
	capTimeout        time.Duration = 100 * time.Millisecond
	bucketSize                      = 1 * time.Minute
	maxAge                          = 24 * time.Hour
	maxHostsPerBucket               = 10000 // cap to bound memory on busy routers

	// Rate ring: short circular buffer for responsive rate calculation.
	// 6 slots × 5s = 30s window. Rates are computed over the filled
	// portion of the ring, so peaks show within 5–10s instead of 60–120s.
	rateSlotDuration = 5 * time.Second
	rateSlotCount    = 6
)

type TalkerKey struct {
	IP string `json:"ip"`
}

type TalkerStat struct {
	IP          string  `json:"ip"`
	Hostname    string  `json:"hostname"`
	Country     string  `json:"country,omitempty"`
	CountryName string  `json:"country_name,omitempty"`
	Latitude    float64 `json:"lat,omitempty"`
	Longitude   float64 `json:"lon,omitempty"`
	ASN         uint    `json:"asn,omitempty"`
	ASOrg       string  `json:"as_org,omitempty"`
	TotalBytes  uint64  `json:"total_bytes"`
	RxBytes     uint64  `json:"rx_bytes"`
	TxBytes     uint64  `json:"tx_bytes"`
	RateBytes   float64 `json:"rate_bytes"`
	RxRate      float64 `json:"rx_rate"`
	TxRate      float64 `json:"tx_rate"`
	Packets     uint64  `json:"packets"`
}

type bucket struct {
	timestamp  time.Time
	hosts      map[string]*hostAccum
	protoBytes map[string]uint64
	ipVerBytes map[string]uint64
}

// rateSlot is one slot in the short rate ring buffer.
type rateSlot struct {
	timestamp time.Time
	hosts     map[string]*hostAccum
}

type hostAccum struct {
	bytes   uint64
	rxBytes uint64 // towards local nets (download)
	txBytes uint64 // from local nets (upload)
	packets uint64
}

type Tracker struct {
	devices     []string
	promiscuous bool
	localNets   []*net.IPNet        // LOCAL_NETS for direction detection
	selfIPs     map[string]struct{} // router's own interface IPs for direction tiebreaker
	lanDevices  map[string]bool     // LAN-facing interfaces (have private addrs) — only these count hosts
	mu          sync.RWMutex
	buckets     []*bucket
	current     *bucket
	stopCh      chan struct{}
	dns         *resolver.Resolver
	geoDB       *geoip.DB

	// Rate ring: short circular buffer (5s slots) for responsive rate calc.
	// Protected by the same mu as buckets/current.
	rateRing    [rateSlotCount]*rateSlot
	rateRingIdx int // index of current slot in rateRing
}

func New(devices []string, promiscuous bool, localNets []*net.IPNet, geoDB *geoip.DB, dns *resolver.Resolver) *Tracker {
	// Build a set of the router's own interface IPs so we can resolve
	// direction when both endpoints fall within localNets (e.g. the
	// router's WAN IP talking to a remote host through a tunnel).
	selfIPs := make(map[string]struct{})
	if ifaces, err := net.Interfaces(); err == nil {
		for _, iface := range ifaces {
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				ipnet, ok := addr.(*net.IPNet)
				if !ok {
					continue
				}
				selfIPs[ipnet.IP.String()] = struct{}{}
			}
		}
	}
	if len(selfIPs) > 0 {
		log.Printf("talkers: %d self IPs for direction detection", len(selfIPs))
	}

	// Identify LAN-facing interfaces: L2 (Ethernet) interfaces that have
	// at least one private (RFC 1918 / ULA) address. Only LAN interfaces
	// count per-host traffic to avoid double-counting packets that traverse
	// multiple interfaces (e.g. WAN → kernel routing → LAN, or tunnel →
	// kernel → LAN).
	//
	// L3 interfaces (WireGuard, PPP, tun) are excluded even if they have
	// private tunnel IPs (e.g. 10.x.x.x) — they are tunnel/WAN endpoints,
	// not LAN segments. Counting on the LAN side gives a single, consistent
	// view of who is talking to whom.
	lanDevices := make(map[string]bool)
	if ifaces, err := net.Interfaces(); err == nil {
		for _, iface := range ifaces {
			if iface.Name == "lo" {
				continue
			}
			// Skip L3 interfaces — tunnels and WAN, never LAN
			if packets.IsL3Device(iface.Name) {
				continue
			}
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				ipnet, ok := addr.(*net.IPNet)
				if !ok {
					continue
				}
				if ipnet.IP.IsPrivate() && !ipnet.IP.IsLinkLocalUnicast() {
					lanDevices[iface.Name] = true
					break
				}
			}
		}
	}
	if len(lanDevices) > 0 {
		names := make([]string, 0, len(lanDevices))
		for name := range lanDevices {
			names = append(names, name)
		}
		log.Printf("talkers: LAN interfaces for host accounting: %s", strings.Join(names, ", "))
	}

	trk := &Tracker{
		devices:     devices,
		promiscuous: promiscuous,
		localNets:   localNets,
		selfIPs:     selfIPs,
		lanDevices:  lanDevices,
		buckets:     make([]*bucket, 0, 1440),
		stopCh:      make(chan struct{}),
		dns:         dns,
		geoDB:       geoDB,
	}
	// Initialize first rate ring slot
	trk.rateRing[0] = &rateSlot{
		timestamp: time.Now(),
		hosts:     make(map[string]*hostAccum),
	}
	return trk
}

func (t *Tracker) Run() {
	devices, err := t.getDevices()
	if err != nil {
		log.Printf("talkers: cannot list devices: %v", err)
		log.Println("talkers: top-talkers feature requires root/CAP_NET_RAW")
		return
	}
	if len(devices) == 0 {
		log.Println("talkers: no capture devices found")
		return
	}

	t.current = &bucket{
		timestamp:  time.Now().Truncate(bucketSize),
		hosts:      make(map[string]*hostAccum),
		protoBytes: make(map[string]uint64),
		ipVerBytes: make(map[string]uint64),
	}

	go t.rotateBuckets()
	go t.rotateRateRing()

	for _, dev := range devices {
		if !t.lanDevices[dev] {
			log.Printf("talkers: skipping capture on %s (not LAN)", dev)
			continue
		}
		go t.captureDevice(dev)
	}

	<-t.stopCh
}

func (t *Tracker) Stop() {
	close(t.stopCh)
}

func (t *Tracker) TopByVolume(n int) []TalkerStat {
	// Step 1: Copy raw data under lock
	t.mu.RLock()
	totals := make(map[string]*TalkerStat)
	for _, b := range t.buckets {
		for ip, acc := range b.hosts {
			if _, ok := totals[ip]; !ok {
				totals[ip] = &TalkerStat{IP: ip}
			}
			totals[ip].TotalBytes += acc.bytes
			totals[ip].RxBytes += acc.rxBytes
			totals[ip].TxBytes += acc.txBytes
			totals[ip].Packets += acc.packets
		}
	}
	if t.current != nil {
		for ip, acc := range t.current.hosts {
			if _, ok := totals[ip]; !ok {
				totals[ip] = &TalkerStat{IP: ip}
			}
			totals[ip].TotalBytes += acc.bytes
			totals[ip].RxBytes += acc.rxBytes
			totals[ip].TxBytes += acc.txBytes
			totals[ip].Packets += acc.packets
		}
	}
	t.mu.RUnlock()

	// Step 2: Sort + trim before enrichment to avoid unnecessary work
	// Only include external IPs in the top talkers list
	list := make([]TalkerStat, 0, len(totals))
	for _, s := range totals {
		ip := net.ParseIP(s.IP)
		if ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
			continue
		}
		// Skip IPs on local subnets (e.g. LAN clients with global IPv6)
		if ip != nil && t.isLocalNet(ip) {
			continue
		}
		// Skip the router's own IPs (WAN, VPN tunnel endpoints, etc)
		if _, isSelf := t.selfIPs[s.IP]; isSelf {
			continue
		}
		list = append(list, *s)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].TotalBytes > list[j].TotalBytes
	})
	if len(list) > n {
		list = list[:n]
	}

	// Step 3: Enrich outside lock — DNS resolution and GeoIP are expensive
	for i := range list {
		if t.dns != nil {
			list[i].Hostname = t.dns.LookupAddrAsync(list[i].IP)
		}
		t.enrichGeo(&list[i])
	}
	return list
}

func (t *Tracker) TopByBandwidth(n int) []TalkerStat {
	// Use the short rate ring (5s slots, ~30s window) for responsive rate
	// calculation. The 1-minute buckets are still used for 24h volume.
	t.mu.RLock()
	if t.current == nil {
		t.mu.RUnlock()
		return nil
	}

	rates, elapsed := t.rateFromRing()
	t.mu.RUnlock()

	// Step 2: Build stats, sort, and trim before enrichment
	// Only include external IPs in the top talkers list
	list := make([]TalkerStat, 0, len(rates))
	for ip, r := range rates {
		parsedIP := net.ParseIP(ip)
		if parsedIP != nil && (parsedIP.IsPrivate() || parsedIP.IsLoopback() || parsedIP.IsLinkLocalUnicast()) {
			continue
		}
		// Skip IPs on local subnets (e.g. LAN clients with global IPv6)
		if parsedIP != nil && t.isLocalNet(parsedIP) {
			continue
		}
		// Skip the router's own IPs (WAN, VPN tunnel endpoints, etc)
		if _, isSelf := t.selfIPs[ip]; isSelf {
			continue
		}
		list = append(list, TalkerStat{
			IP:         ip,
			TotalBytes: r.bytes,
			RxBytes:    r.rxBytes,
			TxBytes:    r.txBytes,
			RateBytes:  float64(r.bytes) / elapsed,
			RxRate:     float64(r.rxBytes) / elapsed,
			TxRate:     float64(r.txBytes) / elapsed,
			Packets:    r.packets,
		})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].RateBytes > list[j].RateBytes
	})
	if len(list) > n {
		list = list[:n]
	}

	// Step 3: Enrich outside lock — DNS resolution and GeoIP are expensive
	for i := range list {
		if t.dns != nil {
			list[i].Hostname = t.dns.LookupAddrAsync(list[i].IP)
		}
		t.enrichGeo(&list[i])
	}
	return list
}

func (t *Tracker) getDevices() ([]string, error) {
	if len(t.devices) > 0 {
		return t.devices, nil
	}

	devs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, d := range devs {
		addrs, err := d.Addrs()
		if err != nil {
			continue
		}
		if d.Name == "lo" || len(addrs) == 0 {
			continue
		}
		names = append(names, d.Name)
	}
	return names, nil
}

func (t *Tracker) captureDevice(device string) {
	ring, err := packets.NewRing(device, t.promiscuous)
	if err != nil {
		log.Printf("talkers: cannot open ring on %s: %v", device, err)
		return
	}
	defer ring.Close()
	log.Printf("talkers: TPACKET_V3 ring on %s", device)

	// Parsed packet for batch processing — stack-allocated, no heap.
	type parsedPkt struct {
		srcStr, dstStr       string
		srcLocal, dstLocal   bool
		srcSelf, dstSelf     bool
		srcLoopLL, dstLoopLL bool
		wireLen              uint64
		proto                string
		ipVersion            string
	}

	// IP string cache: avoids heap-allocating net.IP.String() for every
	// packet. At 10 MB/s there are ~7000 pps but only 10-100 unique IPs.
	// The cache hits 99%+ of lookups, eliminating the main GC bottleneck.
	ipStrCache := make(map[[16]byte]string, 256)
	ipStr := func(ip net.IP) string {
		var k [16]byte
		copy(k[:], ip.To16())
		if s, ok := ipStrCache[k]; ok {
			return s
		}
		s := ip.String()
		ipStrCache[k] = s
		return s
	}

	// Pre-allocate batch buffer (reused across blocks)
	batch := make([]parsedPkt, 0, 256)

	for {
		select {
		case <-t.stopCh:
			return
		default:
		}

		// Phase 1: Parse all packets in the block WITHOUT holding the lock.
		// IP parsing, string conversion, and classification happen here.
		batch = batch[:0]
		ring.ReadBlock(func(pkt []byte, wireLen uint32) {
			ipPacket := packets.ParseIPPacket(pkt, true)
			if ipPacket.Version == 0 || ipPacket.IsTunnel {
				return
			}
			srcIP := ipPacket.SrcIP
			dstIP := ipPacket.DstIP
			srcStr := ipStr(srcIP)
			dstStr := ipStr(dstIP)
			_, srcSelf := t.selfIPs[srcStr]
			_, dstSelf := t.selfIPs[dstStr]

			ipVersion := "IPv4"
			if ipPacket.Version != 4 {
				ipVersion = "IPv6"
			}
			var proto string
			switch ipPacket.Proto {
			case unix.IPPROTO_TCP:
				proto = "TCP"
			case unix.IPPROTO_UDP:
				proto = "UDP"
			case unix.IPPROTO_ICMP, unix.IPPROTO_ICMPV6:
				proto = "ICMP"
			default:
				proto = "Other"
			}

			batch = append(batch, parsedPkt{
				srcStr:    srcStr,
				dstStr:    dstStr,
				srcLocal:  isLocalIP(srcIP) || t.isLocalNet(srcIP),
				dstLocal:  isLocalIP(dstIP) || t.isLocalNet(dstIP),
				srcSelf:   srcSelf,
				dstSelf:   dstSelf,
				srcLoopLL: srcIP.IsLoopback() || srcIP.IsLinkLocalUnicast(),
				dstLoopLL: dstIP.IsLoopback() || dstIP.IsLinkLocalUnicast(),
				wireLen:   uint64(wireLen),
				proto:     proto,
				ipVersion: ipVersion,
			})
		}, 100)

		if len(batch) == 0 {
			continue
		}

		// Phase 2: Apply all parsed packets under ONE lock acquisition.
		// This is ~170 map updates per lock instead of 1, eliminating
		// lock contention with SSE readers.
		t.mu.Lock()
		if t.current == nil {
			t.mu.Unlock()
			continue
		}
		rSlot := t.rateRing[t.rateRingIdx]
		for i := range batch {
			p := &batch[i]
			// Host accounting
			for _, entry := range []struct {
				ip     string
				local  bool
				loopLL bool
			}{
				{p.srcStr, p.srcLocal, p.srcLoopLL},
				{p.dstStr, p.dstLocal, p.dstLoopLL},
			} {
				if entry.loopLL {
					continue
				}
				if _, ok := t.current.hosts[entry.ip]; !ok {
					if len(t.current.hosts) >= maxHostsPerBucket {
						continue
					}
					t.current.hosts[entry.ip] = &hostAccum{}
				}
				t.current.hosts[entry.ip].bytes += p.wireLen
				t.current.hosts[entry.ip].packets++

				// Rate ring: mirror byte + packet accounting
				if rSlot != nil {
					if _, ok := rSlot.hosts[entry.ip]; !ok {
						rSlot.hosts[entry.ip] = &hostAccum{}
					}
					rSlot.hosts[entry.ip].bytes += p.wireLen
					rSlot.hosts[entry.ip].packets++
				}
			}

			// Direction detection
			if len(t.localNets) > 0 {
				if p.srcLocal && !p.dstLocal {
					if h, ok := t.current.hosts[p.dstStr]; ok {
						h.txBytes += p.wireLen
					}
					if rSlot != nil {
						if h, ok := rSlot.hosts[p.dstStr]; ok {
							h.txBytes += p.wireLen
						}
					}
				} else if !p.srcLocal && p.dstLocal {
					if h, ok := t.current.hosts[p.srcStr]; ok {
						h.rxBytes += p.wireLen
					}
					if rSlot != nil {
						if h, ok := rSlot.hosts[p.srcStr]; ok {
							h.rxBytes += p.wireLen
						}
					}
				} else if p.srcLocal && p.dstLocal {
					if p.srcSelf && !p.dstSelf {
						if h, ok := t.current.hosts[p.dstStr]; ok {
							h.txBytes += p.wireLen
						}
						if h, ok := t.current.hosts[p.srcStr]; ok {
							h.txBytes += p.wireLen
						}
						if rSlot != nil {
							if h, ok := rSlot.hosts[p.dstStr]; ok {
								h.txBytes += p.wireLen
							}
							if h, ok := rSlot.hosts[p.srcStr]; ok {
								h.txBytes += p.wireLen
							}
						}
					} else if p.dstSelf && !p.srcSelf {
						if h, ok := t.current.hosts[p.srcStr]; ok {
							h.rxBytes += p.wireLen
						}
						if h, ok := t.current.hosts[p.dstStr]; ok {
							h.rxBytes += p.wireLen
						}
						if rSlot != nil {
							if h, ok := rSlot.hosts[p.srcStr]; ok {
								h.rxBytes += p.wireLen
							}
							if h, ok := rSlot.hosts[p.dstStr]; ok {
								h.rxBytes += p.wireLen
							}
						}
					}
				}
			}
			t.current.protoBytes[p.proto] += p.wireLen
			t.current.ipVerBytes[p.ipVersion] += p.wireLen
		}
		t.mu.Unlock()
	}
}

func (t *Tracker) rotateBuckets() {
	ticker := time.NewTicker(bucketSize)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			t.mu.Lock()
			now := time.Now()
			if t.current != nil {
				t.buckets = append(t.buckets, t.current)
			}
			cutoff := now.Add(-maxAge)
			idx := 0
			for idx < len(t.buckets) && t.buckets[idx].timestamp.Before(cutoff) {
				idx++
			}
			if idx > 0 {
				t.buckets = t.buckets[idx:]
			}
			t.current = &bucket{
				timestamp:  now.Truncate(bucketSize),
				hosts:      make(map[string]*hostAccum),
				protoBytes: make(map[string]uint64),
				ipVerBytes: make(map[string]uint64),
			}
			t.mu.Unlock()
		case <-t.stopCh:
			return
		}
	}
}

// rotateRateRing advances the short rate ring every rateSlotDuration.
func (t *Tracker) rotateRateRing() {
	ticker := time.NewTicker(rateSlotDuration)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			t.mu.Lock()
			t.rateRingIdx = (t.rateRingIdx + 1) % rateSlotCount
			t.rateRing[t.rateRingIdx] = &rateSlot{
				timestamp: time.Now(),
				hosts:     make(map[string]*hostAccum),
			}
			t.mu.Unlock()
		case <-t.stopCh:
			return
		}
	}
}

// rateFromRing computes per-IP rates from the rate ring (excluding the
// current slot which is still accumulating). Returns bytes/elapsed maps.
// Must be called with t.mu held (at least RLock).
func (t *Tracker) rateFromRing() (rates map[string]*hostAccum, elapsed float64) {
	now := time.Now()
	rates = make(map[string]*hostAccum)
	var oldest time.Time

	for i := 0; i < rateSlotCount; i++ {
		slot := t.rateRing[i]
		if slot == nil {
			continue
		}
		// Include all slots (the current one is still accumulating,
		// but including it keeps rates responsive to new bursts).
		if oldest.IsZero() || slot.timestamp.Before(oldest) {
			oldest = slot.timestamp
		}
		for ip, acc := range slot.hosts {
			if e, ok := rates[ip]; ok {
				e.bytes += acc.bytes
				e.rxBytes += acc.rxBytes
				e.txBytes += acc.txBytes
				e.packets += acc.packets
			} else {
				rates[ip] = &hostAccum{
					bytes:   acc.bytes,
					rxBytes: acc.rxBytes,
					txBytes: acc.txBytes,
					packets: acc.packets,
				}
			}
		}
	}

	elapsed = now.Sub(oldest).Seconds()
	if elapsed < 1 {
		elapsed = 1
	}
	return
}

// GetProtocolBreakdown returns accumulated bytes per L4 protocol over the 24h window.
func (t *Tracker) GetProtocolBreakdown() map[string]uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	totals := make(map[string]uint64)
	for _, b := range t.buckets {
		for proto, bytes := range b.protoBytes {
			totals[proto] += bytes
		}
	}
	if t.current != nil {
		for proto, bytes := range t.current.protoBytes {
			totals[proto] += bytes
		}
	}
	return totals
}

// GetIPVersionBreakdown returns accumulated bytes per IP version (IPv4/IPv6) over the 24h window.
func (t *Tracker) GetIPVersionBreakdown() map[string]uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	totals := make(map[string]uint64)
	for _, b := range t.buckets {
		for ver, bytes := range b.ipVerBytes {
			totals[ver] += bytes
		}
	}
	if t.current != nil {
		for ver, bytes := range t.current.ipVerBytes {
			totals[ver] += bytes
		}
	}
	return totals
}

// CountryStat holds per-country traffic totals.
type CountryStat struct {
	Country     string `json:"country"`
	CountryName string `json:"country_name"`
	Bytes       uint64 `json:"bytes"`
	Connections int    `json:"connections"`
}

// ASNStat holds per-ASN traffic totals.
type ASNStat struct {
	ASN         uint   `json:"asn"`
	ASOrg       string `json:"as_org"`
	Bytes       uint64 `json:"bytes"`
	Connections int    `json:"connections"`
}

// enrichGeo populates geo fields on a TalkerStat from the MMDB.
func (t *Tracker) enrichGeo(s *TalkerStat) {
	if t.geoDB == nil {
		return
	}
	geo := t.geoDB.Lookup(s.IP)
	if geo == nil {
		return
	}
	s.Country = geo.Country
	s.CountryName = geo.CountryName
	s.Latitude = geo.Latitude
	s.Longitude = geo.Longitude
	s.ASN = geo.ASN
	s.ASOrg = geo.ASOrg
}

// GeoBreakdown holds both per-country and per-ASN traffic summaries,
// computed in a single pass over the IP totals to avoid duplicate work.
type GeoBreakdown struct {
	Countries []CountryStat `json:"countries"`
	ASNs      []ASNStat     `json:"asns"`
}

// GetGeoBreakdown returns traffic grouped by country and by ASN over the
// 24h window.  Both are computed in a single lock + GeoIP pass.
func (t *Tracker) GetGeoBreakdown() *GeoBreakdown {
	if t.geoDB == nil || !t.geoDB.Available() {
		return &GeoBreakdown{}
	}

	// Step 1: Copy raw data under lock
	t.mu.RLock()
	ipTotals := make(map[string]uint64)
	for _, b := range t.buckets {
		for ip, acc := range b.hosts {
			ipTotals[ip] += acc.bytes
		}
	}
	if t.current != nil {
		for ip, acc := range t.current.hosts {
			ipTotals[ip] += acc.bytes
		}
	}
	t.mu.RUnlock()

	// Step 2: Single GeoIP enrichment pass outside lock
	type countryAcc struct {
		name  string
		bytes uint64
		ips   int
	}
	type asnAcc struct {
		org   string
		bytes uint64
		ips   int
	}
	countries := make(map[string]*countryAcc)
	asns := make(map[uint]*asnAcc)

	for ip, bytes := range ipTotals {
		// Skip local/private/self IPs — they have no GeoIP data and
		// inflate the "Unknown" category.
		parsedIP := net.ParseIP(ip)
		if parsedIP != nil && (parsedIP.IsPrivate() || parsedIP.IsLoopback() || parsedIP.IsLinkLocalUnicast()) {
			continue
		}
		if parsedIP != nil && t.isLocalNet(parsedIP) {
			continue
		}
		if _, isSelf := t.selfIPs[ip]; isSelf {
			continue
		}

		geo := t.geoDB.Lookup(ip)

		// Country aggregation
		cc := "XX"
		cname := "Unknown"
		if geo != nil && geo.Country != "" {
			cc = geo.Country
			cname = geo.CountryName
		}
		if _, ok := countries[cc]; !ok {
			countries[cc] = &countryAcc{name: cname}
		}
		countries[cc].bytes += bytes
		countries[cc].ips++

		// ASN aggregation
		if geo != nil && geo.ASN != 0 {
			if _, ok := asns[geo.ASN]; !ok {
				asns[geo.ASN] = &asnAcc{org: geo.ASOrg}
			}
			asns[geo.ASN].bytes += bytes
			asns[geo.ASN].ips++
		}
	}

	// Build country result
	countryResult := make([]CountryStat, 0, len(countries))
	for cc, acc := range countries {
		countryResult = append(countryResult, CountryStat{
			Country:     cc,
			CountryName: acc.name,
			Bytes:       acc.bytes,
			Connections: acc.ips,
		})
	}
	sort.Slice(countryResult, func(i, j int) bool {
		return countryResult[i].Bytes > countryResult[j].Bytes
	})
	if len(countryResult) > 20 {
		countryResult = countryResult[:20]
	}

	// Build ASN result
	asnResult := make([]ASNStat, 0, len(asns))
	for asn, acc := range asns {
		asnResult = append(asnResult, ASNStat{
			ASN:         asn,
			ASOrg:       acc.org,
			Bytes:       acc.bytes,
			Connections: acc.ips,
		})
	}
	sort.Slice(asnResult, func(i, j int) bool {
		return asnResult[i].Bytes > asnResult[j].Bytes
	})
	if len(asnResult) > 20 {
		asnResult = asnResult[:20]
	}

	return &GeoBreakdown{
		Countries: countryResult,
		ASNs:      asnResult,
	}
}

// isLocalIP checks if an IP is private, loopback, or link-local.
// Uses Go's built-in methods — zero allocations.
func isLocalIP(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

func (t *Tracker) isLocalNet(ip net.IP) bool {
	if len(t.localNets) == 0 {
		return false
	}
	for _, n := range t.localNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// BucketPoint is a single 1-minute data point for a host.
type BucketPoint struct {
	Timestamp int64  `json:"ts"`
	Bytes     uint64 `json:"bytes"`
	RxBytes   uint64 `json:"rx_bytes"`
	TxBytes   uint64 `json:"tx_bytes"`
	Packets   uint64 `json:"packets"`
}

// HostHistory returns the per-minute bandwidth history for a single IP
// over the 24h window. Returns nil if the IP has never been seen.
func (t *Tracker) HostHistory(ip string) []BucketPoint {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var points []BucketPoint
	for _, b := range t.buckets {
		if acc, ok := b.hosts[ip]; ok {
			points = append(points, BucketPoint{
				Timestamp: b.timestamp.UnixMilli(),
				Bytes:     acc.bytes,
				RxBytes:   acc.rxBytes,
				TxBytes:   acc.txBytes,
				Packets:   acc.packets,
			})
		}
	}
	if t.current != nil {
		if acc, ok := t.current.hosts[ip]; ok {
			points = append(points, BucketPoint{
				Timestamp: t.current.timestamp.UnixMilli(),
				Bytes:     acc.bytes,
				RxBytes:   acc.rxBytes,
				TxBytes:   acc.txBytes,
				Packets:   acc.packets,
			})
		}
	}
	return points
}

// HostTotals returns the aggregate traffic stats for a single IP.
// Returns nil if the IP has never been seen.
func (t *Tracker) HostTotals(ip string) *TalkerStat {
	t.mu.RLock()
	var found bool
	stat := &TalkerStat{IP: ip}
	for _, b := range t.buckets {
		if acc, ok := b.hosts[ip]; ok {
			found = true
			stat.TotalBytes += acc.bytes
			stat.RxBytes += acc.rxBytes
			stat.TxBytes += acc.txBytes
			stat.Packets += acc.packets
		}
	}
	if t.current != nil {
		if acc, ok := t.current.hosts[ip]; ok {
			found = true
			stat.TotalBytes += acc.bytes
			stat.RxBytes += acc.rxBytes
			stat.TxBytes += acc.txBytes
			stat.Packets += acc.packets
		}
		// Rate from the short rate ring (5s slots, ~30s window)
		rates, elapsed := t.rateFromRing()
		if r, ok := rates[ip]; ok {
			stat.RateBytes = float64(r.bytes) / elapsed
			stat.RxRate = float64(r.rxBytes) / elapsed
			stat.TxRate = float64(r.txBytes) / elapsed
		}
	}
	t.mu.RUnlock()

	if !found {
		return nil
	}

	// Enrich outside lock
	if t.dns != nil {
		stat.Hostname = t.dns.LookupAddr(ip)
	}
	t.enrichGeo(stat)
	return stat
}

// UniqueIPs returns the number of distinct external IPs seen in the 24h window.
func (t *Tracker) UniqueIPs() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	seen := make(map[string]struct{})
	for _, b := range t.buckets {
		for ip := range b.hosts {
			seen[ip] = struct{}{}
		}
	}
	if t.current != nil {
		for ip := range t.current.hosts {
			seen[ip] = struct{}{}
		}
	}
	return len(seen)
}
