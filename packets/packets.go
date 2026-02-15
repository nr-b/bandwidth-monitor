package packets

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"unsafe"

	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

var (
	SnapLen int32 = 128
	// AnyIpFilter is a BPF program for Layer 2 (Ethernet) interfaces.
	// It accepts IPv4, IPv6, and 802.1Q VLAN-tagged IP frames.
	AnyIpFilter = []bpf.Instruction{
		// Load EtherType at standard Ethernet header offset (12 bytes).
		bpf.LoadAbsolute{Off: 12, Size: 2},
		// If EtherType is IPv4, accept.
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: unix.ETH_P_IP, SkipTrue: 5},
		// If EtherType is IPv6, accept.
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: unix.ETH_P_IPV6, SkipTrue: 4},
		// If EtherType is 802.1Q VLAN, check inner EtherType at offset 16.
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: unix.ETH_P_8021Q, SkipTrue: 1},
		// Not IP and not VLAN: drop.
		bpf.RetConstant{Val: 0},
		// One level of VLAN: load inner EtherType at offset 16.
		bpf.LoadAbsolute{Off: 16, Size: 2},
		// If inner EtherType is IPv4 or IPv6, accept; otherwise drop.
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: unix.ETH_P_IP, SkipTrue: 1},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: unix.ETH_P_IPV6, SkipTrue: 0, SkipFalse: 1},
		// Accept: return snaplen.
		bpf.RetConstant{Val: uint32(SnapLen)},
		// Drop.
		bpf.RetConstant{Val: 0},
	}

	// RawIpFilter is a BPF program for Layer 3 (raw IP) interfaces such as
	// WireGuard, tun, or PPP. There is no Ethernet header; the first byte
	// contains the IP version nibble.
	RawIpFilter = []bpf.Instruction{
		// Load the first byte (IP version + IHL for v4, or version + traffic class for v6).
		bpf.LoadAbsolute{Off: 0, Size: 1},
		// Shift right 4 to isolate the version nibble.
		bpf.ALUOpConstant{Op: bpf.ALUOpShiftRight, Val: 4},
		// If version == 4 (IPv4), accept.
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: 4, SkipTrue: 1},
		// If version == 6 (IPv6), accept; otherwise drop.
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: 6, SkipTrue: 0, SkipFalse: 1},
		// Accept.
		bpf.RetConstant{Val: uint32(SnapLen)},
		// Drop.
		bpf.RetConstant{Val: 0},
	}
)

const (
	EthHeaderSize = 14
	v4ProtoOffset = 23
	v6ProtoOffset = 20
	v6HeaderSize  = 40
)

type Packet struct {
	SrcIP        net.IP
	DstIP        net.IP
	Proto        uint8
	Len          uint64
	Version      int
	SrcInterface string
	Dot1qTag     int
	PktType      uint8 // AF_PACKET pkt_type: 0=HOST, 1=BROADCAST, 2=MULTICAST, 3=OTHERHOST, 4=OUTGOING
	IsTunnel     bool  // true if this packet carries encapsulated tunnel traffic
}

// Tunnel protocol numbers (IP header "protocol" field).
const (
	protoIPIP     = 4   // IPv4-in-IPv4 encapsulation
	protoIPv6inV4 = 41  // IPv6-in-IPv4 (6to4, etc)
	protoGRE      = 47  // Generic Routing Encapsulation
	protoESP      = 50  // IPsec Encapsulating Security Payload
	protoAH       = 51  // IPsec Authentication Header
	protoL2TP     = 115 // Layer 2 Tunnelling Protocol
)

// detectTunnel checks if the parsed packet carries tunnel/VPN traffic.
// It checks the IP protocol field for known tunnel protocols, and for
// UDP packets it inspects the first bytes of the payload to detect
// WireGuard and OpenVPN.
func detectTunnel(pkt []byte, ipHdrStart int, p *Packet) {
	// Check IP protocol field for tunnel protocols
	switch p.Proto {
	case protoIPIP, protoIPv6inV4, protoGRE, protoESP, protoAH, protoL2TP:
		p.IsTunnel = true
		return
	case unix.IPPROTO_UDP:
		// Fall through to UDP payload inspection
	default:
		return
	}

	// UDP payload inspection for WireGuard and OpenVPN.
	// Calculate the offset to the UDP header.
	var udpStart int
	if p.Version == 4 {
		// IPv4: IHL (lower nibble of first byte) * 4
		ihl := int(pkt[ipHdrStart]&0x0F) * 4
		udpStart = ipHdrStart + ihl
	} else {
		// IPv6: fixed 40-byte header (ignoring extension headers for now)
		udpStart = ipHdrStart + 40
	}

	// Need at least UDP header (8 bytes) + 4 bytes of payload
	if udpStart+12 > len(pkt) {
		return
	}

	// Read UDP payload (after 8-byte UDP header)
	payloadStart := udpStart + 8
	if payloadStart+4 > len(pkt) {
		return
	}

	// WireGuard: first byte is message type (1=handshake init, 2=handshake resp,
	// 3=cookie, 4=data), followed by three zero reserved bytes.
	if pkt[payloadStart] >= 1 && pkt[payloadStart] <= 4 &&
		pkt[payloadStart+1] == 0 && pkt[payloadStart+2] == 0 && pkt[payloadStart+3] == 0 {
		p.IsTunnel = true
		return
	}

	// OpenVPN: first byte has opcode in bits 7-3 (values 1-10 are valid
	// control/data opcodes) and key_id in bits 2-0.
	opcode := pkt[payloadStart] >> 3
	if opcode >= 1 && opcode <= 10 {
		// Additional check: OpenVPN data packets (opcode 6,9,10) are followed
		// by a 4-byte peer-id or session-id. Control packets (1-5,7,8) have
		// an 8-byte session ID. Check that we have enough data and the
		// opcode is in the valid range.
		// Since opcode 6 (P_DATA_V1) and 9 (P_DATA_V2) are by far the most
		// common, and false positives are possible for random UDP traffic,
		// require the second byte to look like a plausible session/peer ID
		// (not all zeros, which would indicate random data).
		if payloadStart+8 <= len(pkt) {
			// Valid OpenVPN opcode with enough payload
			p.IsTunnel = true
			return
		}
	}
}

// SLL header size for Linux cooked capture (LINUX_SLL v1).
// AF_PACKET on PPP and similar L3 interfaces delivers packets with this
// 16-byte pseudo-header instead of a real link-layer header.
const sllHeaderSize = 16

// ParseIPPacket attempts to parse an IP packet from a slice of bytes.
// When isL3 is true, the data is treated as a raw IP packet (no Ethernet header),
// as delivered by SOCK_DGRAM on WireGuard/tun/PPP interfaces.
// When isL3 is false, the data is treated as an Ethernet frame.
func ParseIPPacket(pkt []byte, isL3 bool) Packet {
	if len(pkt) < 20 {
		return Packet{}
	}

	if isL3 {
		return parseRawIP(pkt)
	}

	// Ethernet frame
	if len(pkt) < 34 {
		return Packet{}
	}
	return parseEthernetFrame(pkt)
}

// parseRawIP parses a raw IP packet (no Ethernet header).
func parseRawIP(pkt []byte) Packet {
	ret := Packet{}
	ipVer := pkt[0] >> 4
	switch ipVer {
	case 4:
		if len(pkt) < 20 {
			return Packet{}
		}
		ret.Version = 4
		ret.SrcIP = net.IP(pkt[12:16])
		ret.DstIP = net.IP(pkt[16:20])
		ret.Proto = pkt[9]
		ret.Len = uint64(binary.BigEndian.Uint16(pkt[2:4]))
	case 6:
		if len(pkt) < 40 {
			return Packet{}
		}
		ret.Version = 6
		ret.SrcIP = net.IP(pkt[8:24])
		ret.DstIP = net.IP(pkt[24:40])
		ret.Proto = pkt[6]
		ret.Len = uint64(binary.BigEndian.Uint16(pkt[4:6])) + v6HeaderSize
	default:
		return Packet{}
	}
	detectTunnel(pkt, 0, &ret)
	return ret
}

// parseEthernetFrame parses an Ethernet frame, handling optional VLAN tags.
func parseEthernetFrame(pkt []byte) Packet {
	ret := Packet{}
	// Step from no vlan tag, to single vlan tag, to QinQ tags.
	for _, offset := range []int{0, 4, 8} {
		headerOffsets := EthHeaderSize + offset
		pktType := binary.BigEndian.Uint16(pkt[headerOffsets-2 : headerOffsets])
		if offset != 0 {
			// The VLAN TCI sits right after the 802.1Q EtherType marker.
			// For offset=4 (single tag): TCI is at pkt[14:16].
			// For offset=8 (QinQ inner): TCI is at pkt[18:20].
			tciStart := EthHeaderSize + offset - 4
			tci := binary.BigEndian.Uint16(pkt[tciStart : tciStart+2])
			ret.Dot1qTag = int(tci & 0x0FFF)
		}
		switch pktType {
		case unix.ETH_P_IP:
			ret.Version = 4
			ret.SrcIP = net.IP(pkt[headerOffsets+12 : headerOffsets+16])
			ret.DstIP = net.IP(pkt[headerOffsets+16 : headerOffsets+20])
			ret.Proto = uint8(pkt[v4ProtoOffset+offset])
			// IPv4 Total Length field already includes the IP header.
			ret.Len = uint64(binary.BigEndian.Uint16(pkt[headerOffsets+2 : headerOffsets+4]))
			detectTunnel(pkt, headerOffsets, &ret)
			return ret
		case unix.ETH_P_IPV6:
			ret.Version = 6
			ret.SrcIP = net.IP(pkt[headerOffsets+8 : headerOffsets+24])
			ret.DstIP = net.IP(pkt[headerOffsets+24 : headerOffsets+40])
			ret.Proto = uint8(pkt[v6ProtoOffset+offset])
			// IPv6 Payload Length excludes the 40-byte fixed header.
			ret.Len = uint64(binary.BigEndian.Uint16(pkt[headerOffsets+4:headerOffsets+6])) + v6HeaderSize
			detectTunnel(pkt, headerOffsets, &ret)
			return ret
		}
	}
	// Not a recognised EtherType after VLAN unwinding — silently ignore.
	return Packet{}
}

// FetchPcapSock creates a new AF_PACKET socket for capturing traffic on the
// given interface. When promisc is true it enables PACKET_MR_PROMISC so that
// the NIC delivers all frames (required for SPAN/mirror ports).
//
// For L3 interfaces (WireGuard, PPP, tun — ARPHRD_NONE/PPP), SOCK_DGRAM is
// used instead of SOCK_RAW. SOCK_RAW on these interfaces prepends a Linux
// cooked (SLL) header that confuses the raw IP BPF filter and parser.
// SOCK_DGRAM strips the link-layer header and delivers the IP payload directly.
func FetchPcapSock(dev string, promisc bool) (int, error) {
	protocol := uint16(unix.ETH_P_ALL)
	iface, err := net.InterfaceByName(dev)
	if err != nil {
		return -1, err
	}
	addr := &unix.SockaddrLinklayer{
		Protocol: uint16(htons(unix.ETH_P_ALL)),
		Ifindex:  iface.Index,
	}
	// Use SOCK_DGRAM for L3 interfaces to get raw IP without SLL header.
	sockType := unix.SOCK_RAW
	if IsL3Device(dev) {
		sockType = unix.SOCK_DGRAM
	}
	fd, err := unix.Socket(unix.AF_PACKET, sockType, int(htons(protocol)))
	if err != nil {
		return -1, err
	}
	// Increase the socket receive buffer to handle high throughput without
	// dropping packets. Default is ~208KB which fills up at ~10MB/s.
	// 4MB gives ~400ms of buffering at 10MB/s.
	_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUFFORCE, 4*1024*1024)
	if err := unix.Bind(fd, addr); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if promisc {
		mreq := unix.PacketMreq{
			Ifindex: int32(iface.Index),
			Type:    unix.PACKET_MR_PROMISC,
		}
		if err := unix.SetsockoptPacketMreq(fd, unix.SOL_PACKET, unix.PACKET_ADD_MEMBERSHIP, &mreq); err != nil {
			_ = unix.Close(fd)
			return -1, fmt.Errorf("enable promiscuous mode on %s: %w", dev, err)
		}
	}
	return fd, nil
}

// ApplyBPFFilter applies the given BPF filter to the given socket descriptor.
func ApplyBPFFilter(sockFd int, rawBpfFilter []bpf.Instruction) error {
	expr, err := bpf.Assemble(rawBpfFilter)
	if err != nil {
		log.Printf("packets: BPF assemble failed: %v", err)
		return err
	}
	prog := &unix.SockFprog{
		Len:    uint16(len(expr)),
		Filter: (*unix.SockFilter)(unsafe.Pointer(&expr[0])),
	}
	return unix.SetsockoptSockFprog(sockFd, unix.SOL_SOCKET, unix.SO_ATTACH_FILTER, prog)
}

func CreateEpoller(sockFD int) (int, error) {
	unix.SetNonblock(sockFD, true)
	epfd, err := unix.EpollCreate(20)
	if err != nil {
		return -1, err
	}
	event := unix.EpollEvent{
		Events: unix.EPOLLIN,
		Fd:     int32(sockFD),
	}
	if err := unix.EpollCtl(epfd, unix.EPOLL_CTL_ADD, sockFD, &event); err != nil {
		unix.Close(epfd)
		return -1, err
	}
	return epfd, nil
}

func htons(i uint16) uint16 {
	return (i<<8)&0xff00 | i>>8
}

// IsL3Device returns true if the named interface is a Layer 3 (point-to-point,
// no Ethernet header) device such as WireGuard, tun, or PPP. This is detected
// via the interface's ARPHRD type: ARPHRD_NONE (0xFFFE) or ARPHRD_PPP (512)
// indicate L3, while ARPHRD_ETHER (1) indicates L2.
func IsL3Device(dev string) bool {
	iface, err := net.InterfaceByName(dev)
	if err != nil {
		return false
	}
	// net.Interface doesn't expose ARPHRD directly, but point-to-point
	// L3 interfaces (wg, tun, ppp) have the PointToPoint flag set and
	// a zero HardwareAddr (no MAC address).
	if iface.Flags&net.FlagPointToPoint != 0 {
		return true
	}
	if len(iface.HardwareAddr) == 0 {
		return true
	}
	return false
}

// BPFFilterForDevice returns the appropriate BPF filter for the given device:
// RawIpFilter for L3 interfaces, AnyIpFilter for L2 (Ethernet) interfaces.
func BPFFilterForDevice(dev string) []bpf.Instruction {
	if IsL3Device(dev) {
		return RawIpFilter
	}
	return AnyIpFilter
}
