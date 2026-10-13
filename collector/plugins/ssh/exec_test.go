package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshServer is an in-process SSH server that accepts one user, answers the
// allowlisted commands from a fixture and records every executed command.
type sshServer struct {
	ln       net.Listener
	mu       sync.Mutex
	executed []string
}

var linuxFixture = map[string]string{
	"hostname -f":                          "web01.example.com",
	"uname -a":                             "Linux web01 6.1.0-18-amd64 #1 SMP PREEMPT_DYNAMIC Debian 6.1.76-1 x86_64 GNU/Linux",
	"cat /etc/os-release":                  "PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nNAME=\"Debian GNU/Linux\"\nVERSION_ID=\"12\"\nID=debian",
	"cat /proc/uptime":                     "86400.55 170000.10",
	"cat /sys/class/dmi/id/product_uuid":   "4C4C4544-0042-3510-8051-B4C04F4A4E33",
	"cat /sys/class/dmi/id/product_serial": "CZ1234ABCD",
	"cat /sys/class/dmi/id/sys_vendor":     "HPE",
	"cat /sys/class/dmi/id/product_name":   "ProLiant DL360 Gen10",
	"cat /sys/class/dmi/id/bios_version":   "U32",
	"nproc":                                "16",
	"free -b":                              "               total        used\nMem:      67108864000  1000\nSwap:     0 0",
}

func startSSHServer(t *testing.T, user, password string) *sshServer {
	t.Helper()
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == user && string(pw) == password {
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
	t.Cleanup(func() { ln.Close() })
	s := &sshServer{ln: ln}
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go s.serve(conn, cfg)
		}
	}()
	return s
}

func (s *sshServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *sshServer) serve(conn net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		channel, requests, cerr := ch.Accept()
		if cerr != nil {
			continue
		}
		go func() {
			defer channel.Close()
			for req := range requests {
				if req.Type != "exec" {
					_ = req.Reply(false, nil)
					continue
				}
				var payload struct{ Command string }
				_ = ssh.Unmarshal(req.Payload, &payload)
				s.mu.Lock()
				s.executed = append(s.executed, payload.Command)
				s.mu.Unlock()
				_ = req.Reply(true, nil)
				status := uint32(0)
				if out, ok := linuxFixture[payload.Command]; ok {
					fmt.Fprint(channel, out)
				} else {
					status = 127
				}
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
				return
			}
		}()
	}
}

// TestCollectRunsAllowlistedCommands covers WP-152 (DIS-04): with
// credentials the plugin logs in, runs exactly the allowlisted commands and
// fills the DeviceRecord fields.
func TestCollectRunsAllowlistedCommands(t *testing.T) {
	srv := startSSHServer(t, "inventory", "s3cret")
	p := &Plugin{Timeout: 2 * time.Second, Port: srv.port(), KnownHostsFile: filepath.Join(t.TempDir(), "known_hosts")}
	creds := map[string]string{"username": "inventory", "password": "s3cret"}

	rec, err := p.Collect(context.Background(), "127.0.0.1", creds)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if rec.System.Name != "web01.example.com" || rec.Identity.Hostname != "web01" || rec.Identity.FQDN != "web01.example.com" ||
		rec.Identity.SerialNumber != "CZ1234ABCD" || rec.Identity.HardwareUUID != "4c4c4544-0042-3510-8051-b4c04f4a4e33" ||
		rec.Classification.Manufacturer != "HPE" || rec.Classification.Model != "ProLiant DL360 Gen10" ||
		rec.System.OSName != "Debian GNU/Linux" || rec.System.OSVersion != "12" || rec.System.UptimeSeconds != 86400 ||
		rec.System.FirmwareVersion != "U32" {
		t.Errorf("record = %+v", rec)
	}
	if rec.Attributes["cpuCount"] != 16 || rec.Attributes["memoryBytes"] != int64(67108864000) || rec.Attributes["authenticated"] != true {
		t.Errorf("attributes = %v", rec.Attributes)
	}
	if v := rec.Validate(); len(v) > 0 {
		t.Errorf("record breaks the contract: %v", v)
	}

	srv.mu.Lock()
	executed := append([]string(nil), srv.executed...)
	srv.mu.Unlock()
	if len(executed) != len(AllowedCommands) {
		t.Errorf("executed %d commands, want the %d allowlisted: %v", len(executed), len(AllowedCommands), executed)
	}
	for _, cmd := range executed {
		if !isAllowed(cmd) {
			t.Errorf("command outside the allowlist executed: %q", cmd)
		}
	}
	if _, err = run(nil, "rm -rf /", time.Second); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("a command outside the allowlist: %v", err)
	}

	// The host key was pinned on first contact.
	pinned, err := os.ReadFile(p.KnownHostsFile)
	if err != nil || !strings.Contains(string(pinned), "ssh-ed25519") {
		t.Fatalf("known_hosts = %q, %v", pinned, err)
	}
	if _, err = p.Collect(context.Background(), "127.0.0.1", map[string]string{"username": "inventory", "password": "wrong"}); err == nil {
		t.Error("a wrong password must fail")
	}
}

// TestChangedHostKeyIsRefused: another server on the pinned address is
// refused before any credential is sent.
func TestChangedHostKeyIsRefused(t *testing.T) {
	known := filepath.Join(t.TempDir(), "known_hosts")
	first := startSSHServer(t, "inventory", "s3cret")
	p := &Plugin{Timeout: 2 * time.Second, Port: first.port(), KnownHostsFile: known}
	creds := map[string]string{"username": "inventory", "password": "s3cret"}
	if _, err := p.Collect(context.Background(), "127.0.0.1", creds); err != nil {
		t.Fatal(err)
	}
	first.ln.Close()

	// A new server (new host key) on the same host and port.
	second := startSSHServerOn(t, first.port())
	p2 := &Plugin{Timeout: 2 * time.Second, Port: first.port(), KnownHostsFile: known}
	_, err := p2.Collect(context.Background(), "127.0.0.1", creds)
	if err == nil || !strings.Contains(err.Error(), ErrHostKeyChanged.Error()) {
		t.Fatalf("changed host key: %v", err)
	}
	second.mu.Lock()
	defer second.mu.Unlock()
	if len(second.executed) != 0 {
		t.Errorf("commands ran on the impostor: %v", second.executed)
	}
}

func startSSHServerOn(t *testing.T, port int) *sshServer {
	t.Helper()
	_, hostKey, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(hostKey)
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	var ln net.Listener
	var err error
	for range 50 {
		if ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("rebind port %d: %v", port, err)
	}
	t.Cleanup(func() { ln.Close() })
	s := &sshServer{ln: ln}
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go s.serve(conn, cfg)
		}
	}()
	return s
}

func TestClientConfigNeedsASecret(t *testing.T) {
	p := &Plugin{KnownHostsFile: filepath.Join(t.TempDir(), "k")}
	if _, ok, err := p.clientConfig(map[string]string{}); ok || err != nil {
		t.Errorf("no username: ok=%v err=%v", ok, err)
	}
	if _, ok, err := p.clientConfig(map[string]string{"username": "u"}); !ok || err == nil {
		t.Errorf("username without secret: ok=%v err=%v", ok, err)
	}
	if _, _, err := p.clientConfig(map[string]string{"username": "u", "private_key": "garbage"}); err == nil {
		t.Error("an unparsable key must fail")
	}
}
