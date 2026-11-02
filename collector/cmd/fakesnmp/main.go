// Command fakesnmp runs the SNMP fake of the real-stack tests
// frontend/e2e-real/onboarding-bundle.spec.ts (WP-384, SNMPv2c) and
// frontend/e2e-real/discovery-errors.spec.ts (WP-542, SNMPv3; package
// internal/fakesnmp). frontend/e2e-real/support/stack.ts builds and starts
// it; it is not part of the collector release build (./cmd/collector).
//
// Usage: fakesnmp (-community <community> | -v3-user <user>
// -v3-auth-password … -v3-priv-password … [-v3-auth-protocol SHA256]
// [-v3-priv-protocol AES]) [-listen 127.0.0.161:161] [-sysname …]
// [-sysdescr …] [-sysobjectid …] [-requests <file>]
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
	"strings"
	"sync"
	"syscall"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakesnmp"
)

// The SNMPv3 protocols the fake accepts, named like the credential fields
// auth_protocol and priv_protocol of the collector (plugins/snmp/v3.go).
var (
	authProtocols = map[string]gosnmp.SnmpV3AuthProtocol{"SHA": gosnmp.SHA, "SHA224": gosnmp.SHA224, "SHA256": gosnmp.SHA256,
		"SHA384": gosnmp.SHA384, "SHA512": gosnmp.SHA512}
	privProtocols = map[string]gosnmp.SnmpV3PrivProtocol{"AES": gosnmp.AES, "AES192": gosnmp.AES192, "AES256": gosnmp.AES256}
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fakesnmp:", err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", "127.0.0.161:161", "UDP address to listen on")
	community := flag.String("community", "", "the only community the fake answers (SNMPv2c)")
	v3User := flag.String("v3-user", "", "the SNMPv3 user the fake answers (authPriv)")
	v3AuthProtocol := flag.String("v3-auth-protocol", "SHA256", "SNMPv3 authentication protocol (SHA, SHA224, SHA256, SHA384, SHA512)")
	v3AuthPassword := flag.String("v3-auth-password", "", "SNMPv3 authentication passphrase of the user")
	v3PrivProtocol := flag.String("v3-priv-protocol", "AES", "SNMPv3 privacy protocol (AES, AES192, AES256)")
	v3PrivPassword := flag.String("v3-priv-password", "", "SNMPv3 privacy passphrase of the user")
	sysName := flag.String("sysname", "fake-snmp", "sysName.0")
	sysDescr := flag.String("sysdescr", "Reticora SNMP fake", "sysDescr.0")
	sysObjectID := flag.String("sysobjectid", ".1.3.6.1.4.1.8072.3.2.10", "sysObjectID.0")
	requests := flag.String("requests", "", "file the received requests are appended to (JSON lines)")
	flag.Parse()

	var v3 *fakesnmp.V3
	if *v3User != "" {
		auth, ok := authProtocols[strings.ToUpper(*v3AuthProtocol)]
		if !ok {
			return fmt.Errorf("-v3-auth-protocol %q: not one of SHA, SHA224, SHA256, SHA384, SHA512", *v3AuthProtocol)
		}
		priv, ok := privProtocols[strings.ToUpper(*v3PrivProtocol)]
		if !ok {
			return fmt.Errorf("-v3-priv-protocol %q: not one of AES, AES192, AES256", *v3PrivProtocol)
		}
		v3 = &fakesnmp.V3{User: *v3User, AuthProtocol: auth, AuthPassphrase: *v3AuthPassword, PrivProtocol: priv, PrivPassphrase: *v3PrivPassword}
	}

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
	cfg := fakesnmp.Config{
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
	}
	var agent *fakesnmp.Agent
	var err error
	if v3 != nil {
		agent, err = fakesnmp.ListenV3(*listen, v3, cfg)
	} else {
		agent, err = fakesnmp.Listen(*listen, cfg)
	}
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
