package icmputil

import (
"net"
"time"

"golang.org/x/net/icmp"
"golang.org/x/net/ipv4"
"golang.org/x/net/ipv6"
)

// PingOne sends a single ICMP echo and waits for the matching reply.
// Returns RTT in ms, or -1 on timeout/error.
// proto: 1 = ICMPv4, 58 = ICMPv6.
func PingOne(conn *icmp.PacketConn, dest net.IP, id, seq uint16, proto int, timeout time.Duration) float64 {
	var msgType, replyType icmp.Type
	if proto == 1 {
		msgType = ipv4.ICMPTypeEcho
		replyType = ipv4.ICMPTypeEchoReply
	} else {
		msgType = ipv6.ICMPTypeEchoRequest
		replyType = ipv6.ICMPTypeEchoReply
	}

	msg := icmp.Message{
		Type: msgType, Code: 0,
		Body: &icmp.Echo{ID: int(id), Seq: int(seq), Data: []byte("bwmon")},
	}
	wb, err := msg.Marshal(nil)
	if err != nil {
		return -1
	}

	dst := &net.IPAddr{IP: dest}
	conn.SetDeadline(time.Now().Add(timeout))
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
		rtt := RTTMs(start)
		rm, err := icmp.ParseMessage(proto, rb[:n])
		if err != nil {
			continue
		}
		if rm.Type == replyType {
			if echo, ok := rm.Body.(*icmp.Echo); ok {
				if uint16(echo.ID) == id {
					return rtt
				}
			}
		}
	}
}

// RTTMs returns milliseconds elapsed since start.
func RTTMs(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000.0
}
