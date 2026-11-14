package config

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/ssh"
)

// deviceServer is an in-process SSH server of a network device (pattern of
// startSSHServer in collector/plugins/ssh): its identification string, the
// answers per command and every executed command.
type deviceServer struct {
	ln       net.Listener
	mu       sync.Mutex
	outputs  map[string]string
	executed []string
}

func startDevice(t *testing.T, serverVersion string, outputs map[string]string) *deviceServer {
	t.Helper()
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &gossh.ServerConfig{
		ServerVersion: serverVersion,
		PasswordCallback: func(c gossh.ConnMetadata, pw []byte) (*gossh.Permissions, error) {
			if c.User() == "netops" && string(pw) == "s3cret" {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	d := &deviceServer{ln: ln, outputs: outputs}
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go d.serve(conn, cfg)
		}
	}()
	return d
}

func (d *deviceServer) serve(conn net.Conn, cfg *gossh.ServerConfig) {
	_, chans, reqs, err := gossh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	go gossh.DiscardRequests(reqs)
	for ch := range chans {
		channel, requests, cerr := ch.Accept()
		if cerr != nil {
			continue
		}
		go func() {
			defer channel.Close() //nolint:errcheck // test server
			for req := range requests {
				if req.Type != "exec" {
					_ = req.Reply(false, nil)
					continue
				}
				var payload struct{ Command string }
				_ = gossh.Unmarshal(req.Payload, &payload)
				d.mu.Lock()
				d.executed = append(d.executed, payload.Command)
				out, ok := d.outputs[payload.Command]
				d.mu.Unlock()
				_ = req.Reply(true, nil)
				status := uint32(0)
				if ok {
					_, _ = io.WriteString(channel, out)
				} else {
					status = 127
				}
				_, _ = channel.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{status}))
				return
			}
		}()
	}
}

func (d *deviceServer) commands() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.executed...)
}

func (d *deviceServer) plugin(t *testing.T) *ssh.Plugin {
	t.Helper()
	return &ssh.Plugin{Timeout: 3 * time.Second, Port: d.ln.Addr().(*net.TCPAddr).Port,
		KnownHostsFile: filepath.Join(t.TempDir(), "known_hosts")}
}

var deviceCreds = map[string]string{"username": "netops", "password": "s3cret"}

// TestDetectPlatform covers WP-1053 AK1: the banner decides RouterOS and
// Huawei without a command, show version decides NX-OS, IOS and Junos; an
// unknown platform is unsupported_platform without a further command.
func TestDetectPlatform(t *testing.T) {
	cases := []struct {
		name    string
		banner  string
		version string
		want    Platform
		cmds    []string
	}{
		{"mikrotik banner", "SSH-2.0-ROSSSH", "", RouterOS, nil},
		{"huawei banner", "SSH-2.0-HUAWEI-1.5", "", HuaweiVRP, nil},
		{"nx-os", "SSH-2.0-OpenSSH_8.8", "Cisco Nexus Operating System (NX-OS) Software\nNXOS: version 9.3(10)", CiscoNXOS, []string{"show version"}},
		{"ios-xe", "SSH-2.0-Cisco-1.25", "Cisco IOS XE Software, Version 17.09.04a\nCisco IOS Software [Cupertino]", CiscoIOS, []string{"show version"}},
		{"junos", "SSH-2.0-OpenSSH_8.6", "Hostname: mx-edge1\nModel: mx204\nJunos: 22.4R3-S2\nJUNOS OS Kernel 64-bit", Junos, []string{"show version"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outputs := map[string]string{}
			if tc.version != "" {
				outputs["show version"] = tc.version
			}
			d := startDevice(t, tc.banner, outputs)
			got, err := Detect(context.Background(), d.plugin(t), "127.0.0.1", deviceCreds)
			if err != nil || got != tc.want {
				t.Fatalf("Detect = %q, %v; want %q", got, err, tc.want)
			}
			if cmds := d.commands(); strings.Join(cmds, ";") != strings.Join(tc.cmds, ";") {
				t.Fatalf("commands = %q, want %q", cmds, tc.cmds)
			}
		})
	}
	t.Run("unsupported", func(t *testing.T) {
		d := startDevice(t, "SSH-2.0-OpenSSH_9.6", map[string]string{"show version": "Linux host 6.1.0", "show running-config": "x"})
		_, err := Collect(context.Background(), d.plugin(t), "127.0.0.1", deviceCreds, "")
		if !errors.Is(err, ErrUnsupportedPlatform) || err.Error() != "unsupported_platform" {
			t.Fatalf("err = %v, want unsupported_platform", err)
		}
		if cmds := d.commands(); len(cmds) != 1 || cmds[0] != "show version" {
			t.Fatalf("commands = %q, want only show version", cmds)
		}
	})
	t.Run("collect with known platform", func(t *testing.T) {
		d := startDevice(t, "SSH-2.0-HUAWEI-1.5", map[string]string{"display current-configuration": "sysname HW1\r\n local-user a password cipher X1\r\n"})
		snap, err := Collect(context.Background(), d.plugin(t), "127.0.0.1", deviceCreds, HuaweiVRP)
		if err != nil || snap.Platform != HuaweiVRP || snap.MaskedCount != 1 || strings.Contains(snap.Text, "X1") {
			t.Fatalf("snapshot = %+v, %v", snap, err)
		}
		if cmds := d.commands(); len(cmds) != 1 || cmds[0] != "display current-configuration" {
			t.Fatalf("commands = %q", cmds)
		}
	})
	// Every command of the platforms is on the allowlist of the SSH plugin.
	for _, p := range Platforms {
		found := false
		for _, c := range ssh.ConfigCommands {
			found = found || c == p.Command()
		}
		if !found {
			t.Errorf("%s: %q not in ssh.ConfigCommands", p, p.Command())
		}
	}
}

const iosConfig = "Building configuration...\r\n\r\nCurrent configuration : %d bytes\r\n" +
	"! Last configuration change at %s by admin\r\n! NVRAM config last updated at %s by admin\r\n" +
	"hostname %s\r\nenable secret 9 $9$abc\r\nntp clock-period %d\r\nend\r\n"

func iosText(size int, at, host string, clock int) string {
	return fmt.Sprintf(iosConfig, size, at, at, host, clock)
}

// TestHashIgnoresVolatileLines covers WP-1053 AK3: the same configuration
// with other time stamps, sizes and clock period has the same hash.
func TestHashIgnoresVolatileLines(t *testing.T) {
	a, err := Process(CiscoIOS, iosText(1234, "08:00:00 UTC Thu Nov 14 2026", "r1", 17179812))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Process(CiscoIOS, strings.ReplaceAll(iosText(1250, "09:30:00 UTC Fri Nov 15 2026", "r1", 17179900), "\r\n", "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA256 != b.SHA256 {
		t.Fatalf("hash differs:\n%s\n---\n%s", a.Text, b.Text)
	}
	for _, p := range []struct {
		platform Platform
		x, y     string
	}{
		{CiscoNXOS, "!Time: Thu Nov 14 08:05:00 2026\nhostname nx1\n", "!Time: Fri Nov 15 10:00:00 2026\nhostname nx1\n"},
		{Junos, "## Last commit: 2026-11-14 08:00:00 UTC by admin\nset system host-name mx1\n", "## Last commit: 2026-11-15 10:00:00 UTC by ops\nset system host-name mx1\n"},
		{RouterOS, "# nov/14/2026 08:00:00 by RouterOS 7.12\n/system identity set name=rb1\n", "# 2026-11-15 10:00:00 by RouterOS 7.16\n/system identity set name=rb1\n"},
		{HuaweiVRP, "!Last configuration was updated at 2026-11-14 08:00:00+00:00\nsysname HW1\n", "!Last configuration was updated at 2026-11-15 10:00:00+00:00\nsysname HW1\n"},
	} {
		x, _ := Process(p.platform, p.x)
		y, _ := Process(p.platform, p.y)
		if x.SHA256 != y.SHA256 {
			t.Errorf("%s: volatile line changes the hash", p.platform)
		}
	}
}

// TestHashChangesOnRealChange covers WP-1053 AK3: a changed line of the
// configuration changes the hash.
func TestHashChangesOnRealChange(t *testing.T) {
	a, _ := Process(CiscoIOS, iosText(1234, "08:00:00 UTC Thu Nov 14 2026", "r1", 1))
	b, _ := Process(CiscoIOS, iosText(1234, "08:00:00 UTC Thu Nov 14 2026", "r2", 1))
	if a.SHA256 == b.SHA256 {
		t.Fatal("a changed hostname keeps the hash")
	}
	if _, err := Process(CiscoIOS, strings.Repeat("x", MaxBytes+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over 2 MiB: %v", err)
	}
}
