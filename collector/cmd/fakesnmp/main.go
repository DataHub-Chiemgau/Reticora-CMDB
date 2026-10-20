// Command fakesnmp runs the SNMPv2c fake of the real-stack test
// frontend/e2e-real/onboarding-bundle.spec.ts (WP-384, package
// internal/fakesnmp). frontend/e2e-real/support/stack.ts builds and starts
// it; it is not part of the collector release build (./cmd/collector).
//
// Usage: fakesnmp -community <community> [-listen 127.0.0.161:161]
// [-sysname …] [-sysdescr …] [-sysobjectid …] [-requests <file>]
//
// The collector's SNMP plugin queries port 161, so the fake binds it; an
// unprivileged process needs net.ipv4.ip_unprivileged_port_start <= 161.
// Once bound it prints "fakesnmp listening on <addr>"; every request it
// receives (answered or not) is appended to the requests file as one JSON
// line.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakesnmp"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fakesnmp:", err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", "127.0.0.161:161", "UDP address to listen on")
	community := flag.String("community", "", "the only community the fake answers (required)")
	sysName := flag.String("sysname", "fake-snmp", "sysName.0")
	sysDescr := flag.String("sysdescr", "Reticora SNMP fake", "sysDescr.0")
	sysObjectID := flag.String("sysobjectid", ".1.3.6.1.4.1.8072.3.2.10", "sysObjectID.0")
	requests := flag.String("requests", "", "file the received requests are appended to (JSON lines)")
	flag.Parse()

	var mu sync.Mutex
	var out *os.File
	if *requests != "" {
		f, err := os.OpenFile(*requests, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	agent, err := fakesnmp.Listen(*listen, fakesnmp.Config{
		Community: *community, SysName: *sysName, SysDescr: *sysDescr, SysObjectID: *sysObjectID,
		OnRequest: func(r fakesnmp.Request) {
			if out == nil {
				return
			}
			line, _ := json.Marshal(r)
			mu.Lock()
			defer mu.Unlock()
			_, _ = out.Write(append(line, '\n'))
		},
	})
	if err != nil {
		return fmt.Errorf("listen %s: %w (a port below 1024 needs net.ipv4.ip_unprivileged_port_start <= the port)", *listen, err)
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		_ = agent.Close()
	}()
	fmt.Printf("fakesnmp listening on %s\n", agent.Addr())
	return agent.Serve()
}
