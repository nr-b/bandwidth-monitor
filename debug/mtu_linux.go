package debug

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// setDontFragment enables the Don't Fragment bit on the IPv4 socket by
// setting IP_MTU_DISCOVER to IP_PMTUDISC_DO on Linux.
func setDontFragment(pc net.PacketConn) {
	sc, ok := pc.(syscall.Conn)
	if !ok {
		return
	}
	rawConn, err := sc.SyscallConn()
	if err != nil {
		return
	}
	rawConn.Control(func(fd uintptr) {
		unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_MTU_DISCOVER, unix.IP_PMTUDISC_DO)
	})
}
