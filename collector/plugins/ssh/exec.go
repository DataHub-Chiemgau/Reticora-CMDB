package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Command execution (DIS-04): with credentials the plugin logs in and runs
// read-only inventory commands. Only the commands of AllowedCommands are
// ever executed; the command text never comes from the target, a profile or
// the server. Host keys are pinned: a known_hosts file
// (RETICORA_SSH_KNOWN_HOSTS) is checked, unknown hosts are trusted on first
// use and recorded there, a changed key is refused.

// AllowedCommands are the only commands the plugin executes (name → command).
var AllowedCommands = map[string]string{
	"hostname":   "hostname -f",
	"uname":      "uname -a",
	"os_release": "cat /etc/os-release",
	"uptime":     "cat /proc/uptime",
	"machine_id": "cat /sys/class/dmi/id/product_uuid",
	"serial":     "cat /sys/class/dmi/id/product_serial",
	"mfg":        "cat /sys/class/dmi/id/sys_vendor",
	"model":      "cat /sys/class/dmi/id/product_name",
	"bios":       "cat /sys/class/dmi/id/bios_version",
	"cpu":        "nproc",
	"memory":     "free -b",
}

// maxOutput caps what one command may return.
const maxOutput = 64 << 10

// ErrNotAllowed refuses a command outside AllowedCommands.
var ErrNotAllowed = errors.New("ssh: command is not on the allowlist")

// ErrHostKeyChanged refuses a host whose key differs from the pinned one.
var ErrHostKeyChanged = errors.New("ssh: host key changed; refusing to connect")

// clientConfig builds the login from creds: username with password and/or
// private_key (PEM, optional passphrase). ok is false without a username.
func (p *Plugin) clientConfig(creds map[string]string) (cfg *ssh.ClientConfig, ok bool, err error) {
	user := strings.TrimSpace(creds["username"])
	if user == "" {
		return nil, false, nil
	}
	var auth []ssh.AuthMethod
	if key := creds["private_key"]; key != "" {
		var signer ssh.Signer
		if pass := creds["passphrase"]; pass != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(key), []byte(pass))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(key))
		}
		if err != nil {
			return nil, true, fmt.Errorf("ssh: private key: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if pw := creds["password"]; pw != "" {
		auth = append(auth, ssh.Password(pw))
	}
	if len(auth) == 0 {
		return nil, true, errors.New("ssh: a password or private key is required")
	}
	return &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: p.hostKeys().callback,
		Timeout:         p.timeout(),
	}, true, nil
}

func (p *Plugin) timeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return defaultTimeout
}

func (p *Plugin) port() int {
	if p.Port > 0 {
		return p.Port
	}
	return defaultPort
}

// hostKeyStore pins host keys in a known_hosts file (trust on first use).
type hostKeyStore struct {
	mu   sync.Mutex
	path string
}

func (p *Plugin) hostKeys() *hostKeyStore {
	p.keysOnce.Do(func() {
		path := p.KnownHostsFile
		if path == "" {
			path = os.Getenv("RETICORA_SSH_KNOWN_HOSTS")
		}
		if path == "" {
			path = "/var/lib/reticora-collector/ssh_known_hosts"
		}
		p.keys = &hostKeyStore{path: path}
	})
	return p.keys
}

func (s *hostKeyStore) callback(hostname string, remote net.Addr, key ssh.PublicKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.path); err == nil {
		check, err := knownhosts.New(s.path)
		if err != nil {
			return fmt.Errorf("ssh: known hosts: %w", err)
		}
		err = check(hostname, remote, key)
		var keyErr *knownhosts.KeyError
		switch {
		case err == nil:
			return nil
		case errors.As(err, &keyErr) && len(keyErr.Want) > 0:
			return ErrHostKeyChanged
		case !errors.As(err, &keyErr):
			return err
		}
	}
	// First contact: pin the key.
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("ssh: known hosts: %w", err)
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("ssh: known hosts: %w", err)
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key))
	return err
}

// runAllowed logs in and runs the allowlisted commands; it returns their
// output by name (commands that fail are left out).
func (p *Plugin) runAllowed(ctx context.Context, target string, cfg *ssh.ClientConfig) (map[string]string, error) {
	addr := net.JoinHostPort(target, strconv.Itoa(p.port()))
	d := net.Dialer{Timeout: p.timeout()}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("ssh: connect %s: %w", target, err)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh: login %s: %w", target, err)
	}
	client := ssh.NewClient(c, chans, reqs)
	defer client.Close()

	names := make([]string, 0, len(AllowedCommands))
	for name := range AllowedCommands {
		names = append(names, name)
	}
	sort.Strings(names)
	out := map[string]string{}
	for _, name := range names {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if res, rerr := run(client, AllowedCommands[name], p.timeout()); rerr == nil {
			out[name] = res
		}
	}
	return out, nil
}

// run executes one command; only allowlisted command texts pass.
func run(client *ssh.Client, command string, timeout time.Duration) (string, error) {
	if !isAllowed(command) {
		return "", ErrNotAllowed
	}
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	var stdout limitedBuffer
	session.Stdout = &stdout
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()
	select {
	case err = <-done:
	case <-time.After(timeout):
		_ = session.Signal(ssh.SIGKILL)
		return "", fmt.Errorf("ssh: %q timed out", command)
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}

func isAllowed(command string) bool {
	for _, allowed := range AllowedCommands {
		if command == allowed {
			return true
		}
	}
	return false
}

// limitedBuffer keeps the first maxOutput bytes.
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := maxOutput - b.Len(); room > 0 {
		if len(p) > room {
			b.Buffer.Write(p[:room])
		} else {
			b.Buffer.Write(p)
		}
	}
	return len(p), nil
}

// osRelease parses /etc/os-release.
func osRelease(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			out[k] = strings.Trim(v, `"'`)
		}
	}
	return out
}
