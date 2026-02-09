package collector

import (
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/gopacket/gopacket/pcap"
)

const (
	spanSnapshotLen = 128 // only need IP headers for direction + length
	spanCapTimeout  = 100 * time.Millisecond
)

// spanOverlay captures packets on a SPAN/mirror port and classifies traffic
// direction using LOCAL_NETS. It feeds direction-aware RX/TX counters back
// to the main Collector, which uses them to override the /proc/net/dev
// numbers for the SPAN device.
type spanOverlay struct {
	device    string
	promisc   bool
	localNets []*net.IPNet

	accMu     sync.Mutex
	rxBytes   uint64
	txBytes   uint64
	rxPackets uint64
	txPackets uint64

	stopCh chan struct{}
}

func newSpanOverlay(device string, promisc bool, localNets []*net.IPNet) *spanOverlay {
	return &spanOverlay{
		device:    device,
		promisc:   promisc,
		localNets: localNets,
		stopCh:    make(chan struct{}),
	}
}

// run opens the capture device and classifies packets until stopped.
func (s *spanOverlay) run() {
	handle, err := pcap.OpenLive(s.device, int32(spanSnapshotLen), s.promisc, spanCapTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "span: cannot open %s: %v\n", s.device, err)
		fmt.Fprintln(os.Stderr, "span: pcap requires root or CAP_NET_RAW")
		return
	}
	defer handle.Close()

	if err := handle.SetBPFFilter("ip or ip6"); err != nil {
		fmt.Fprintf(os.Stderr, "span: BPF filter error: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "span: capturing on %s (promiscuous=%v, %d local nets)\n",
		s.device, s.promisc, len(s.localNets))

	for {
		select {
		case <-s.stopCh:
			return
		default:
		}
		data, _, err := handle.ReadPacketData()
		if err != nil {
			if err == pcap.NextErrorTimeoutExpired {
				continue
			}
			fmt.Fprintf(os.Stderr, "span: read error on %s: %v\n", s.device, err)
			return
		}
		pkt := gopacket.NewPacket(data, handle.LinkType(), gopacket.DecodeOptions{
			Lazy:   true,
			NoCopy: true,
		})
		s.processPacket(pkt)
	}
}

func (s *spanOverlay) stop() {
	close(s.stopCh)
}

// snapshot returns the current cumulative counters.
func (s *spanOverlay) snapshot() (rxBytes, txBytes, rxPackets, txPackets uint64) {
	s.accMu.Lock()
	rxBytes = s.rxBytes
	txBytes = s.txBytes
	rxPackets = s.rxPackets
	txPackets = s.txPackets
	s.accMu.Unlock()
	return
}

func (s *spanOverlay) processPacket(pkt gopacket.Packet) {
	var srcIP, dstIP net.IP
	var pktLen uint64

	if ipLayer := pkt.Layer(layers.LayerTypeIPv4); ipLayer != nil {
		ip := ipLayer.(*layers.IPv4)
		srcIP = ip.SrcIP
		dstIP = ip.DstIP
		pktLen = uint64(ip.Length)
	} else if ipLayer := pkt.Layer(layers.LayerTypeIPv6); ipLayer != nil {
		ip := ipLayer.(*layers.IPv6)
		srcIP = ip.SrcIP
		dstIP = ip.DstIP
		pktLen = uint64(ip.Length) + 40 // IPv6 payload length excludes the 40-byte header
	} else {
		return
	}

	srcLocal := s.isLocal(srcIP)
	dstLocal := s.isLocal(dstIP)

	s.accMu.Lock()
	switch {
	case srcLocal && !dstLocal:
		// local → remote = upload (TX)
		s.txBytes += pktLen
		s.txPackets++
	case !srcLocal && dstLocal:
		// remote → local = download (RX)
		s.rxBytes += pktLen
		s.rxPackets++
	case srcLocal && dstLocal:
		// intra-LAN — count as both
		s.rxBytes += pktLen
		s.rxPackets++
		s.txBytes += pktLen
		s.txPackets++
	}
	// both-remote packets (shouldn't appear on a properly-filtered SPAN) are ignored
	s.accMu.Unlock()
}

func (s *spanOverlay) isLocal(ip net.IP) bool {
	for _, n := range s.localNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
