package snmp

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

const (
	v3TestEngineID = "\x80\x00\x1f\x88\x80reticora-test"
	v3AuthPass     = "auth-passphrase-978"
	v3PrivPass     = "priv-passphrase-978"
)

// v3Sender describes the trap the test sends with gosnmp.
type v3Sender struct {
	user      string
	flags     gosnmp.SnmpV3MsgFlags
	authProto gosnmp.SnmpV3AuthProtocol
	authPass  string
	privProto gosnmp.SnmpV3PrivProtocol
	privPass  string
	engineID  string
	boots     uint32
	time      uint32
}

func defaultSender() v3Sender {
	return v3Sender{user: "trapuser", flags: gosnmp.AuthPriv, authProto: gosnmp.SHA256, authPass: v3AuthPass,
		privProto: gosnmp.AES, privPass: v3PrivPass, engineID: v3TestEngineID, boots: 3, time: 1000}
}

// buildV3Trap sends a linkDown trap with gosnmp to a local socket and
// returns the datagram (known keys, fixed engine ID).
func buildV3Trap(t *testing.T, s *v3Sender) []byte {
	t.Helper()
	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	g := &gosnmp.GoSNMP{
		Target: "127.0.0.1", Port: uint16(ln.LocalAddr().(*net.UDPAddr).Port), Transport: "udp", //nolint:gosec // a port
		Version: gosnmp.Version3, SecurityModel: gosnmp.UserSecurityModel, MsgFlags: s.flags, Timeout: time.Second,
		SecurityParameters: &gosnmp.UsmSecurityParameters{
			UserName: s.user, AuthoritativeEngineID: s.engineID,
			AuthoritativeEngineBoots: s.boots, AuthoritativeEngineTime: s.time,
			AuthenticationProtocol: s.authProto, AuthenticationPassphrase: s.authPass,
			PrivacyProtocol: s.privProto, PrivacyPassphrase: s.privPass,
		},
	}
	if s.flags == gosnmp.NoAuthNoPriv {
		g.SecurityParameters = &gosnmp.UsmSecurityParameters{UserName: s.user, AuthoritativeEngineID: s.engineID,
			AuthoritativeEngineBoots: s.boots, AuthoritativeEngineTime: s.time}
	}
	if s.flags == gosnmp.AuthNoPriv {
		usm := g.SecurityParameters.(*gosnmp.UsmSecurityParameters)
		usm.PrivacyProtocol, usm.PrivacyPassphrase = gosnmp.NoPriv, ""
	}
	if cerr := g.Connect(); cerr != nil {
		t.Fatal(cerr)
	}
	defer func() { _ = g.Conn.Close() }()
	_, err = g.SendTrap(gosnmp.SnmpTrap{Variables: []gosnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.3.0", Type: gosnmp.TimeTicks, Value: uint32(4242)},
		{Name: ".1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.ObjectIdentifier, Value: ".1.3.6.1.6.3.1.1.5.3"},
		{Name: ".1.3.6.1.2.1.2.2.1.1.7", Type: gosnmp.Integer, Value: 7},
		{Name: ".1.3.6.1.2.1.2.2.1.2.7", Type: gosnmp.OctetString, Value: []byte("ge-0/0/7")},
	}})
	if err != nil {
		t.Fatalf("send trap: %v", err)
	}
	buf := make([]byte, 65535)
	_ = ln.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := ln.ReadFrom(buf)
	if err != nil {
		t.Fatalf("read trap: %v", err)
	}
	return append([]byte(nil), buf[:n]...)
}

func usmOf(users ...map[string]string) *TrapUSM {
	return &TrapUSM{Users: func() []map[string]string { return users }}
}

func trapUserCreds(name, authProto, authPass, privProto, privPass string) map[string]string {
	m := map[string]string{"username": name, "auth_protocol": authProto, "auth_password": authPass}
	if privPass != "" {
		m["priv_protocol"], m["priv_password"] = privProto, privPass
	}
	return m
}

func wantDrop(t *testing.T, err error, reason string) {
	t.Helper()
	var drop *TrapDropError
	if !errors.As(err, &drop) || drop.Reason != reason {
		t.Fatalf("err = %v, want drop %s", err, reason)
	}
}

func checkLinkDown(t *testing.T, ev *TrapEvent, user string) {
	t.Helper()
	if ev.User != user || ev.Community != "" || ev.TrapOID != "1.3.6.1.6.3.1.1.5.3" {
		t.Fatalf("event = %+v", ev)
	}
	if ev.Variables["1.3.6.1.2.1.2.2.1.1.7"] != "7" || ev.Variables["1.3.6.1.2.1.2.2.1.2.7"] != "ge-0/0/7" ||
		ev.Variables["1.3.6.1.2.1.1.3.0"] != "4242" {
		t.Fatalf("variables = %v", ev.Variables)
	}
	if _, ok := ev.Variables["1.3.6.1.6.3.1.1.4.1.0"]; ok {
		t.Fatal("snmpTrapOID.0 listed as a variable")
	}
}

// TestV3TrapAuthNoPriv covers AK1 and AK4: SHA-256 without privacy gives
// the event of a v2c trap with the user name.
func TestV3TrapAuthNoPriv(t *testing.T) {
	s := defaultSender()
	s.flags = gosnmp.AuthNoPriv
	raw := buildV3Trap(t, &s)
	ev, err := parseTrap(raw, usmOf(trapUserCreds("trapuser", "SHA-256", v3AuthPass, "", "")))
	if err != nil {
		t.Fatal(err)
	}
	checkLinkDown(t, ev, "trapuser")
}

// TestV3TrapAuthPriv covers AK1 and AK4: SHA-256 with AES-128.
func TestV3TrapAuthPriv(t *testing.T) {
	s := defaultSender()
	raw := buildV3Trap(t, &s)
	ev, err := parseTrap(raw, usmOf(trapUserCreds("trapuser", "sha256", v3AuthPass, "aes128", v3PrivPass)))
	if err != nil {
		t.Fatal(err)
	}
	checkLinkDown(t, ev, "trapuser")
	// A wrong privacy passphrase cannot decrypt the ScopedPDU.
	_, err = parseTrap(raw, usmOf(trapUserCreds("trapuser", "sha256", v3AuthPass, "aes128", "another-priv-pass")))
	wantDrop(t, err, TrapDropDecrypt)
	// An encrypted trap for a user without privacy key.
	_, err = parseTrap(raw, usmOf(trapUserCreds("trapuser", "sha256", v3AuthPass, "", "")))
	wantDrop(t, err, TrapDropDecrypt)
}

// TestV3TrapWrongKeyDropped covers AK3: a wrong authentication key drops
// the trap with reason auth.
func TestV3TrapWrongKeyDropped(t *testing.T) {
	s := defaultSender()
	raw := buildV3Trap(t, &s)
	_, err := parseTrap(raw, usmOf(trapUserCreds("trapuser", "sha256", "wrong-auth-passphrase", "aes", v3PrivPass)))
	wantDrop(t, err, TrapDropAuth)
	// A wrong protocol is a wrong key as well.
	_, err = parseTrap(raw, usmOf(trapUserCreds("trapuser", "sha512", v3AuthPass, "aes", v3PrivPass)))
	wantDrop(t, err, TrapDropAuth)
}

// TestV3TrapUnknownUserDropped covers AK3: a user without credential, and
// any v3 trap without a USM, is unknown_user.
func TestV3TrapUnknownUserDropped(t *testing.T) {
	s := defaultSender()
	raw := buildV3Trap(t, &s)
	_, err := parseTrap(raw, usmOf(trapUserCreds("someone-else", "sha256", v3AuthPass, "aes", v3PrivPass)))
	wantDrop(t, err, TrapDropUnknownUser)
	_, err = parseTrap(raw, nil)
	wantDrop(t, err, TrapDropUnknownUser)
}

// TestV3TrapNoAuthNoPrivRejected covers AK1: noAuthNoPriv traps and users
// with MD5 or DES are refused with security_level.
func TestV3TrapNoAuthNoPrivRejected(t *testing.T) {
	s := defaultSender()
	s.flags = gosnmp.NoAuthNoPriv
	raw := buildV3Trap(t, &s)
	_, err := parseTrap(raw, usmOf(trapUserCreds("trapuser", "sha256", v3AuthPass, "", "")))
	wantDrop(t, err, TrapDropSecurityLevel)

	md5 := defaultSender()
	md5.flags, md5.authProto = gosnmp.AuthNoPriv, gosnmp.MD5
	_, err = parseTrap(buildV3Trap(t, &md5), usmOf(trapUserCreds("trapuser", "md5", v3AuthPass, "", "")))
	wantDrop(t, err, TrapDropSecurityLevel)
	des := defaultSender()
	des.privProto = gosnmp.DES
	_, err = parseTrap(buildV3Trap(t, &des), usmOf(trapUserCreds("trapuser", "sha256", v3AuthPass, "des", v3PrivPass)))
	wantDrop(t, err, TrapDropSecurityLevel)
	// An authPriv user does not accept a trap without privacy.
	plain := defaultSender()
	plain.flags = gosnmp.AuthNoPriv
	_, err = parseTrap(buildV3Trap(t, &plain), usmOf(trapUserCreds("trapuser", "sha256", v3AuthPass, "aes", v3PrivPass)))
	wantDrop(t, err, TrapDropSecurityLevel)
}

// TestV3TrapReplayOutsideTimeWindow covers AK2: a trap replayed more than
// 150 s after the engine time it carries is dropped; boots going back as
// well.
func TestV3TrapReplayOutsideTimeWindow(t *testing.T) {
	s := defaultSender()
	raw := buildV3Trap(t, &s)
	var mu sync.Mutex
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	usm := usmOf(trapUserCreds("trapuser", "sha256", v3AuthPass, "aes", v3PrivPass))
	usm.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	if _, err := parseTrap(raw, usm); err != nil {
		t.Fatal(err)
	}
	advance(100 * time.Second)
	if _, err := parseTrap(raw, usm); err != nil {
		t.Fatalf("replay within 150 s: %v", err)
	}
	advance(100 * time.Second) // 200 s after the engine time of the trap
	_, err := parseTrap(raw, usm)
	wantDrop(t, err, TrapDropNotInTime)
	// A newer trap of the engine is accepted again.
	newer := s
	newer.time = 1200
	if _, perr := parseTrap(buildV3Trap(t, &newer), usm); perr != nil {
		t.Fatalf("current trap: %v", perr)
	}
	// Boots going back.
	older := s
	older.boots, older.time = 2, 5000
	_, err = parseTrap(buildV3Trap(t, &older), usm)
	wantDrop(t, err, TrapDropNotInTime)
	// A forged trap with a wrong key does not move the engine state.
	forged := s
	forged.authPass, forged.time = "forged-passphrase", 9_000_000
	_, err = parseTrap(buildV3Trap(t, &forged), usm)
	wantDrop(t, err, TrapDropAuth)
	if _, err := parseTrap(buildV3Trap(t, &newer), usm); err != nil {
		t.Fatalf("after a forged trap: %v", err)
	}
	// Authentication comes before the time window (RFC 3414 3.2 steps 6
	// and 7): a forged trap with old boots counts as auth.
	forgedOld := s
	forgedOld.authPass, forgedOld.boots = "forged-passphrase", 1
	_, err = parseTrap(buildV3Trap(t, &forgedOld), usm)
	wantDrop(t, err, TrapDropAuth)
}

// TestV3TrapSameUserNameTwoCredentials covers AK1: two snmp_v3 credentials
// with one user name and other passwords (one per site); a trap signed
// with either is accepted, one with neither dropped as auth.
func TestV3TrapSameUserNameTwoCredentials(t *testing.T) {
	usm := usmOf(
		trapUserCreds("trapuser", "sha256", "site-a-auth-pass", "aes", "site-a-priv-pass"),
		trapUserCreds("trapuser", "sha256", v3AuthPass, "aes", v3PrivPass),
	)
	s := defaultSender()
	ev, err := parseTrap(buildV3Trap(t, &s), usm)
	if err != nil {
		t.Fatalf("trap of the second credential: %v", err)
	}
	checkLinkDown(t, ev, "trapuser")
	// Same authentication, another privacy key: the user that decrypts
	// the trap counts.
	usm = usmOf(
		trapUserCreds("trapuser", "sha256", v3AuthPass, "aes", "other-priv-pass"),
		trapUserCreds("trapuser", "sha256", v3AuthPass, "aes", v3PrivPass),
	)
	if _, err := parseTrap(buildV3Trap(t, &s), usm); err != nil {
		t.Fatalf("trap of the second privacy key: %v", err)
	}
	other := s
	other.authPass = "neither-auth-pass"
	_, err = parseTrap(buildV3Trap(t, &other), usm)
	wantDrop(t, err, TrapDropAuth)
}

// TestV3TrapKeyCacheBounded covers AK2: keys are localized per (user,
// engine ID) and at most 1,024 entries are kept, the least recently used
// leave first.
func TestV3TrapKeyCacheBounded(t *testing.T) {
	usm := usmOf(trapUserCreds("trapuser", "sha256", v3AuthPass, "aes", v3PrivPass))
	user, err := trapUserFromCreds(trapUserCreds("trapuser", "sha256", v3AuthPass, "aes", v3PrivPass))
	if err != nil {
		t.Fatal(err)
	}
	first, err := usm.securityParams(user, "engine-0000")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < TrapKeyCacheSize+100; i++ {
		if _, serr := usm.securityParams(user, fmt.Sprintf("engine-%04d", i)); serr != nil {
			t.Fatal(serr)
		}
	}
	if n := usm.KeyCacheLen(); n != TrapKeyCacheSize {
		t.Fatalf("cache holds %d keys, want %d", n, TrapKeyCacheSize)
	}
	again, _ := usm.securityParams(user, "engine-0000")
	if again == first {
		t.Fatal("the least recently used key was not evicted")
	}
	// Keys of two engines differ (localized per engine ID).
	a, _ := usm.securityParams(user, "engine-a")
	b, _ := usm.securityParams(user, "engine-b")
	if string(a.SecretKey) == string(b.SecretKey) {
		t.Fatal("keys are not localized per engine")
	}
	// Traps still verify after the evictions.
	s := defaultSender()
	ev, err := parseTrap(buildV3Trap(t, &s), usm)
	if err != nil {
		t.Fatal(err)
	}
	checkLinkDown(t, ev, "trapuser")
}

// TestV3TrapNoKeyMaterialInDropLog covers AK3: the drop error carries the
// reason only.
func TestV3TrapNoKeyMaterialInDropLog(t *testing.T) {
	s := defaultSender()
	_, err := parseTrap(buildV3Trap(t, &s), usmOf(trapUserCreds("trapuser", "sha256", "wrong-auth-passphrase", "aes", v3PrivPass)))
	for _, secret := range []string{v3AuthPass, v3PrivPass, "wrong-auth-passphrase"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error %q leaks a passphrase", err)
		}
	}
}
