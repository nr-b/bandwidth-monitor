//go:build !linux

package debug

import "net"

// setDontFragment is a no-op on non-Linux platforms.
// On macOS/BSD the DF bit is typically set by default for ICMP echo,
// and the "message too long" error from WriteTo covers local MTU.
func setDontFragment(_ net.PacketConn) {}
