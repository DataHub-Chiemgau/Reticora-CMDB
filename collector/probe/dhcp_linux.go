//go:build linux

package probe

import "syscall"

// dhcpBindDevice binds the socket to the interface (SO_BINDTODEVICE) and
// allows broadcasts (mode discover of the dhcp probe).
func dhcpBindDevice(fd uintptr, iface string) error {
	if err := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1); err != nil {
		return err
	}
	return syscall.BindToDevice(int(fd), iface)
}
