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
}

// ParseIPPacket attempts to parse an IP packet from a slice of bytes.
// It handles both Layer 2 frames (with Ethernet header) and raw Layer 3
// packets (e.g. from WireGuard/tun interfaces captured via AF_PACKET).
func ParseIPPacket(pkt []byte) Packet {
	if len(pkt) < 20 {
		return Packet{}
	}

	// Detect raw IP packets (no Ethernet header) by checking the IP
	// version nibble in the first byte. AF_PACKET on L3 interfaces
	// (WireGuard, tun) delivers packets without an Ethernet header.
	ipVer := pkt[0] >> 4
	if ipVer == 4 || ipVer == 6 {
		return parseRawIP(pkt)
	}

	// Otherwise assume a standard Ethernet frame.
	if len(pkt) < 48 {
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
			return ret
		case unix.ETH_P_IPV6:
			ret.Version = 6
			ret.SrcIP = net.IP(pkt[headerOffsets+8 : headerOffsets+24])
			ret.DstIP = net.IP(pkt[headerOffsets+24 : headerOffsets+40])
			ret.Proto = uint8(pkt[v6ProtoOffset+offset])
			// IPv6 Payload Length excludes the 40-byte fixed header.
			ret.Len = uint64(binary.BigEndian.Uint16(pkt[headerOffsets+4:headerOffsets+6])) + v6HeaderSize
			return ret
		}
	}
	// Not a recognised EtherType after VLAN unwinding — silently ignore.
	return Packet{}
}

// FetchPcapSock creates a new AF_PACKET socket for capturing traffic on the
// given interface. When promisc is true it enables PACKET_MR_PROMISC so that
// the NIC delivers all frames (required for SPAN/mirror ports).
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
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, int(htons(protocol)))
	if err != nil {
		return -1, err
	}
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
