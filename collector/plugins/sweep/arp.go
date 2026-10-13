package sweep

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
)

// ARP (DIS-01): the ICMP and TCP probes make the kernel resolve every
// target of a directly attached subnet via ARP. A host that answers ARP but
// drops ICMP and all probed ports is still present in the neighbour table
// with its MAC address. The sweep reads that table (Linux: /proc/net/arp;
// no extra capability needed); targets behind a router never appear there.

// NeighborTable returns the resolved IPv4 neighbours (IP → MAC).
type NeighborTable func() (map[string]string, error)

// ProcNeighbors reads /proc/net/arp.
func ProcNeighbors() (map[string]string, error) {
	f, err := os.Open("/proc/net/arp")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseARPTable(f)
}

// atfComplete is the ATF_COM flag of a resolved entry.
const atfComplete = 0x2

// parseARPTable parses the /proc/net/arp format:
//
//	IP address       HW type     Flags       HW address            Mask     Device
//	192.168.1.1      0x1         0x2         aa:bb:cc:dd:ee:ff     *        eth0
func parseARPTable(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		flags, err := strconv.ParseUint(strings.TrimPrefix(fields[2], "0x"), 16, 32)
		if err != nil || flags&atfComplete == 0 || fields[3] == "00:00:00:00:00:00" {
			continue
		}
		out[fields[0]] = strings.ToLower(fields[3])
	}
	return out, sc.Err()
}
