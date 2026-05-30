// Package netutil provides network utilities for Helix Cluster OS.
package netutil

import "net"

// GetLocalIP returns the first non-loopback local IP address.
func GetLocalIP() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String(), nil
			}
		}
	}
	return "127.0.0.1", nil
}

// IsValidPort checks if a port number is in the valid range.
func IsValidPort(port int) bool {
	return port > 0 && port <= 65535
}
