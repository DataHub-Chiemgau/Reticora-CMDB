//go:build !linux

package probe

import "errors"

// dhcpBindDevice is Linux only: binding a socket to an interface for the
// broadcast of mode discover needs SO_BINDTODEVICE. Elsewhere the bind
// fails, which the dhcp probe reports as error/unsupported.
func dhcpBindDevice(_ uintptr, _ string) error {
	return errors.New("dhcp: mode discover needs SO_BINDTODEVICE (Linux)")
}
