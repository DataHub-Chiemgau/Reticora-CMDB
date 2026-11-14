package snmp

import (
	"bytes"
	"container/list"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"
)

// SNMPv3 traps (DIS-06, WP-978): the receiver accepts traps of the
// user-based security model at the levels authNoPriv and authPriv. The
// users come from the snmp_v3 credentials of the trap configuration of the
// collector group (WP-977) with the field names of the polling
// (v3FromCreds): username, auth_protocol, auth_password, priv_protocol and
// priv_password; without priv_password the user is authNoPriv.
// Authentication SHA and SHA-224/256/384/512, encryption AES-128/192/256
// (the tables of v3.go); MD5, DES and noAuthNoPriv are refused.
//
// The sender of a trap is the authoritative engine: the keys are localized
// per (user, msgAuthoritativeEngineID) after RFC 3414 and kept in a bounded
// cache (TrapKeyCacheSize entries, least recently used out). Per engine ID
// the receiver keeps engineBoots and engineTime and checks the time window
// of RFC 3414 section 3.2: a trap whose engine time lies more than 150 s
// behind the estimated engine time, or whose boots went back, is dropped.
// No key material is logged.

// Reasons of a dropped SNMPv3 trap (traps_dropped in the alive message,
// next to probewire.TrapDropCommunity).
const (
	TrapDropSecurityLevel = "security_level"
	TrapDropNotInTime     = "not_in_time_window"
	TrapDropAuth          = "auth"
	TrapDropUnknownUser   = "unknown_user"
	TrapDropDecrypt       = "decrypt"
)

// TrapKeyCacheSize bounds the localized keys (and the engines) the
// receiver keeps.
const TrapKeyCacheSize = 1024

// trapTimeWindow is the time window of RFC 3414 section 3.2 (150 s).
const trapTimeWindow = 150 * time.Second

// maxEngineBoots is 2^31-1: an engine at this boot count is never in time.
const maxEngineBoots = 2147483647

// TrapDropError is a trap dropped for a reason that is counted.
type TrapDropError struct{ Reason string }

func (e *TrapDropError) Error() string { return "snmp trap dropped: " + e.Reason }

func dropErr(reason string) error { return &TrapDropError{Reason: reason} }

// TrapUSM holds the SNMPv3 users of the receiver, the localized keys and
// the time state per engine. It is safe for concurrent use.
type TrapUSM struct {
	// Users returns the credentials of the configured snmp_v3 users (field
	// names of v3FromCreds); it is called per trap, so changed
	// credentials apply without a restart.
	Users func() []map[string]string
	// Now replaces the clock of the time window (tests).
	Now func() time.Time

	mu      sync.Mutex
	keys    lruCache[*gosnmp.UsmSecurityParameters]
	engines lruCache[*engineState]
}

// engineState is the last authentic boots/time of an engine and when it
// was seen.
type engineState struct {
	boots, time uint32
	seen        time.Time
}

// trapUser is a usable trap user.
type trapUser struct {
	name      string
	authProto gosnmp.SnmpV3AuthProtocol
	authPass  string
	privProto gosnmp.SnmpV3PrivProtocol
	privPass  string
}

// level is the security level the user requires.
func (u *trapUser) level() gosnmp.SnmpV3MsgFlags {
	if u.privPass != "" {
		return gosnmp.AuthPriv
	}
	return gosnmp.AuthNoPriv
}

// fingerprint identifies the user's configuration in the key cache, so a
// changed password or protocol is localized again; it is a hash, never
// the secrets themselves.
func (u *trapUser) fingerprint(engineID string) string {
	h := sha256.New()
	for _, s := range []string{u.name, u.authProto.String(), u.authPass, u.privProto.String(), u.privPass, engineID} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// errTrapUserLevel refuses a trap user below authNoPriv or with MD5/DES.
var errTrapUserLevel = errors.New("snmp: trap user needs SHA/SHA-2 authentication and, with privacy, AES")

// trapUserFromCreds reads a trap user (AK1): username and auth_password
// are required, priv_password optional (authNoPriv without it).
func trapUserFromCreds(creds map[string]string) (*trapUser, error) {
	u := &trapUser{
		name:     credential(creds, "username", "user"),
		authPass: credential(creds, "auth_password", "authKey", "authPassword"),
		privPass: credential(creds, "priv_password", "privKey", "privPassword"),
	}
	if u.name == "" || u.authPass == "" {
		return nil, errTrapUserLevel
	}
	authName := strings.ReplaceAll(strings.ToLower(credential(creds, "auth_protocol", "authProtocol")), "-", "")
	if authName == "" {
		authName = "sha256"
	}
	var ok bool
	if u.authProto, ok = authProtocols[authName]; !ok {
		return nil, errTrapUserLevel
	}
	if u.privPass == "" {
		u.privProto = gosnmp.NoPriv
		return u, nil
	}
	privName := strings.ReplaceAll(strings.ToLower(credential(creds, "priv_protocol", "privProtocol")), "-", "")
	if privName == "" {
		privName = "aes"
	}
	if u.privProto, ok = privProtocols[privName]; !ok {
		return nil, errTrapUserLevel
	}
	return u, nil
}

func (s *TrapUSM) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// lookup returns every configured user of name: in USM a user is the pair
// (engine ID, name), so several credentials may share a name with other
// passwords or protocols. bad is true when the name is configured but only
// with a refused security level.
func (s *TrapUSM) lookup(name string) (users []*trapUser, bad bool) {
	if s == nil || s.Users == nil {
		return nil, false
	}
	for _, creds := range s.Users() {
		if credential(creds, "username", "user") != name {
			continue
		}
		user, err := trapUserFromCreds(creds)
		if err != nil {
			bad = true
			continue
		}
		users = append(users, user)
	}
	if len(users) > 0 {
		bad = false
	}
	return users, bad
}

// securityParams returns the localized keys of (user, engine ID) from the
// cache or localizes them (RFC 3414 A.2).
func (s *TrapUSM) securityParams(u *trapUser, engineID string) (*gosnmp.UsmSecurityParameters, error) {
	key := u.fingerprint(engineID)
	s.mu.Lock()
	sp, ok := s.keys.get(key)
	s.mu.Unlock()
	if ok {
		return sp, nil
	}
	sp = &gosnmp.UsmSecurityParameters{
		UserName: u.name, AuthoritativeEngineID: engineID,
		AuthenticationProtocol: u.authProto, AuthenticationPassphrase: u.authPass,
		PrivacyProtocol: u.privProto, PrivacyPassphrase: u.privPass,
		Logger: gosnmp.NewLogger(nil),
	}
	if err := sp.InitSecurityKeys(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.keys.put(key, sp)
	s.mu.Unlock()
	return sp, nil
}

// KeyCacheLen is the number of localized keys held (tests).
func (s *TrapUSM) KeyCacheLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keys.len()
}

// inTimeWindow checks boots/time of an engine against its last authentic
// values (RFC 3414 section 3.2 step 7b, non-authoritative receiver).
func (s *TrapUSM) inTimeWindow(engineID string, boots, engineTime uint32, now time.Time) bool {
	if boots >= maxEngineBoots {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.engines.get(engineID)
	if !ok {
		return true
	}
	switch {
	case boots < st.boots:
		return false
	case boots > st.boots:
		return true
	}
	estimated := float64(st.time) + now.Sub(st.seen).Seconds()
	return float64(engineTime) >= estimated-trapTimeWindow.Seconds()
}

// remember stores boots/time of an authentic trap when they are newer.
func (s *TrapUSM) remember(engineID string, boots, engineTime uint32, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.engines.get(engineID)
	if ok && (boots < st.boots || (boots == st.boots && engineTime <= st.time)) {
		return
	}
	s.engines.put(engineID, &engineState{boots: boots, time: engineTime, seen: now})
}

// parseV3Trap authenticates, decrypts and decodes an SNMPv3 trap (AK1 to
// AK4). A dropped trap returns a *TrapDropError with the reason.
func parseV3Trap(raw []byte, usm *TrapUSM) (*TrapEvent, error) {
	head, _ := decodeV3(raw, "trap")
	if head == nil {
		return nil, errors.New("snmp: not an SNMPv3 message")
	}
	hsp, ok := head.SecurityParameters.(*gosnmp.UsmSecurityParameters)
	if !ok || head.SecurityModel != gosnmp.UserSecurityModel {
		return nil, dropErr(TrapDropSecurityLevel)
	}
	flags := head.MsgFlags & gosnmp.AuthPriv
	if flags&gosnmp.AuthNoPriv == 0 {
		return nil, dropErr(TrapDropSecurityLevel) // noAuthNoPriv
	}
	users, bad := usm.lookup(hsp.UserName)
	switch {
	case len(users) == 0 && bad:
		return nil, dropErr(TrapDropSecurityLevel)
	case len(users) == 0:
		return nil, dropErr(TrapDropUnknownUser)
	}
	// The users of the name whose level fits the message; without one the
	// reason comes from the first user.
	var fitting []*trapUser
	for _, u := range users {
		if u.level() == flags {
			fitting = append(fitting, u)
		}
	}
	if len(fitting) == 0 {
		if users[0].level() == gosnmp.AuthPriv {
			// The user requires privacy; a plaintext trap is below its
			// level.
			return nil, dropErr(TrapDropSecurityLevel)
		}
		// Encrypted, but the user has no privacy key.
		return nil, dropErr(TrapDropDecrypt)
	}
	engineID := hsp.AuthoritativeEngineID
	// Authentication first, then timeliness (RFC 3414 section 3.2 steps 6
	// and 7): a forged trap without the key counts as auth, whatever its
	// boots/time. Every user whose key verifies the HMAC may be the
	// sender.
	type candidate struct {
		user *trapUser
		sp   *gosnmp.UsmSecurityParameters
	}
	var authenticated []candidate
	for _, u := range fitting {
		p, err := usm.securityParams(u, engineID)
		if err == nil && authentic(raw, hsp.AuthenticationParameters, p) {
			authenticated = append(authenticated, candidate{u, p})
		}
	}
	if len(authenticated) == 0 {
		return nil, dropErr(TrapDropAuth)
	}
	now := usm.now()
	if !usm.inTimeWindow(engineID, hsp.AuthoritativeEngineBoots, hsp.AuthoritativeEngineTime, now) {
		return nil, dropErr(TrapDropNotInTime)
	}
	// Authentic: what fails now is the decryption of the ScopedPDU (or
	// its content); the first user whose privacy key decrypts it is the
	// sender.
	var user *trapUser
	var pkt *gosnmp.SnmpPacket
	for _, c := range authenticated {
		if p, err := unmarshalV3Trap(raw, c.sp, flags); err == nil {
			user, pkt = c.user, p
			break
		}
	}
	if pkt == nil {
		if flags == gosnmp.AuthPriv {
			return nil, dropErr(TrapDropDecrypt)
		}
		return nil, dropErr(TrapDropAuth)
	}
	if pkt.PDUType != gosnmp.SNMPv2Trap {
		return nil, errors.New("snmp: SNMPv3 message is no trap")
	}
	usm.remember(engineID, hsp.AuthoritativeEngineBoots, hsp.AuthoritativeEngineTime, now)
	ev := &TrapEvent{User: user.name, Variables: map[string]string{}, Received: time.Now().UTC()}
	for i := range pkt.Variables {
		v := &pkt.Variables[i]
		name := strings.TrimPrefix(v.Name, ".")
		ev.Variables[name] = pduValueString(v)
	}
	if oid, ok := ev.Variables["1.3.6.1.6.3.1.1.4.1.0"]; ok { // snmpTrapOID.0
		ev.TrapOID = oid
		delete(ev.Variables, "1.3.6.1.6.3.1.1.4.1.0")
	}
	return ev, nil
}

// authDigestLen is the length of msgAuthenticationParameters per protocol
// (RFC 3414: HMAC-SHA-96; RFC 7860: HMAC-SHA-2 truncated).
var authDigestLen = map[gosnmp.SnmpV3AuthProtocol]int{
	gosnmp.SHA: 12, gosnmp.SHA224: 16, gosnmp.SHA256: 24, gosnmp.SHA384: 32, gosnmp.SHA512: 48,
}

// authentic verifies msgAuthenticationParameters (RFC 3414 section 3.2
// step 6): the HMAC with the localized key over the whole message with the
// parameters set to zeros. A digest of the wrong length (another protocol)
// is not authentic.
func authentic(raw []byte, params string, sp *gosnmp.UsmSecurityParameters) bool {
	want := authDigestLen[sp.AuthenticationProtocol]
	if want == 0 || len(params) != want || len(sp.SecretKey) == 0 {
		return false
	}
	field := append([]byte{0x04, byte(want)}, params...)
	at := bytes.Index(raw, field)
	if at < 0 {
		return false
	}
	msg := slices.Clone(raw)
	clear(msg[at+2 : at+2+want])
	mac := hmac.New(sp.AuthenticationProtocol.HashType().New, sp.SecretKey)
	mac.Write(msg)
	return hmac.Equal(mac.Sum(nil)[:want], []byte(params))
}

// unmarshalV3Trap verifies the digest and decrypts with the localized keys.
// gosnmp decrypts in place, so it works on a copy.
func unmarshalV3Trap(raw []byte, sp *gosnmp.UsmSecurityParameters, flags gosnmp.SnmpV3MsgFlags) (pkt *gosnmp.SnmpPacket, err error) {
	defer func() {
		if recover() != nil {
			pkt, err = nil, errors.New("snmp: malformed SNMPv3 trap")
		}
	}()
	x := &gosnmp.GoSNMP{
		Version: gosnmp.Version3, SecurityModel: gosnmp.UserSecurityModel, MsgFlags: flags,
		SecurityParameters: sp.Copy(), Logger: gosnmp.NewLogger(nil),
	}
	return x.UnmarshalTrap(slices.Clone(raw), true)
}

// pduValueString renders a decoded varbind like berValueString renders a
// v1/v2c one: OIDs without the leading dot, numbers in decimal, printable
// byte strings as text and others in hex.
func pduValueString(v *gosnmp.SnmpPDU) string {
	switch v.Type {
	case gosnmp.ObjectIdentifier:
		s, _ := v.Value.(string)
		return strings.TrimPrefix(s, ".")
	case gosnmp.OctetString:
		b, _ := v.Value.([]byte)
		if isPrintable(string(b)) && len(b) > 0 {
			return string(b)
		}
		return fmt.Sprintf("0x%x", b)
	case gosnmp.IPAddress:
		s, _ := v.Value.(string)
		return s
	case gosnmp.Null, gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView:
		return ""
	}
	return gosnmp.ToBigInt(v.Value).String()
}

// logV3Drop logs a dropped v3 trap without key material.
func logV3Drop(source, reason string) {
	slog.Debug("snmp trap dropped", "source", source, "version", 3, "reason", reason)
}

// lruCache is a small least-recently-used map of at most TrapKeyCacheSize
// entries; the caller holds the lock.
type lruCache[V any] struct {
	order *list.List
	items map[string]*list.Element
}

type lruEntry[V any] struct {
	key   string
	value V
}

func (c *lruCache[V]) init() {
	if c.items == nil {
		c.order = list.New()
		c.items = map[string]*list.Element{}
	}
}

func (c *lruCache[V]) get(key string) (V, bool) {
	c.init()
	e, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	c.order.MoveToFront(e)
	return e.Value.(*lruEntry[V]).value, true
}

func (c *lruCache[V]) put(key string, v V) {
	c.init()
	if e, ok := c.items[key]; ok {
		e.Value.(*lruEntry[V]).value = v
		c.order.MoveToFront(e)
		return
	}
	c.items[key] = c.order.PushFront(&lruEntry[V]{key: key, value: v})
	for c.order.Len() > TrapKeyCacheSize {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.items, last.Value.(*lruEntry[V]).key)
	}
}

func (c *lruCache[V]) len() int {
	c.init()
	return c.order.Len()
}
