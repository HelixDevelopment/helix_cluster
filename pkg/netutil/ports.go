package netutil

import "net"

// FreeTCPPort asks the kernel for a free, ephemeral TCP port by binding to
// 127.0.0.1:0 and reading back the chosen port. The listener is closed before
// returning, so the caller can immediately re-bind the port. There is an
// inherent (small) race window between this function returning and the caller
// re-binding; callers that need stronger guarantees should bind :0 themselves
// and use the live listener.
func FreeTCPPort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// FreeUDPPort asks the kernel for a free, ephemeral UDP port by binding to
// 127.0.0.1:0 and reading back the chosen port. The socket is closed before
// returning so the caller can re-bind it. The same small race caveat as
// FreeTCPPort applies.
func FreeUDPPort() (int, error) {
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	c, err := net.ListenUDP("udp", addr)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port, nil
}
