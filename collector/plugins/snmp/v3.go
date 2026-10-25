package snmp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// SNMPv3 (DIS-04): the collector speaks SNMPv3 with the user-based security
// model at security level authPriv only – authentication (SHA-1/SHA-2) and
// encryption (AES) are mandatory; noAuth, authNoPriv, MD5 and DES are
// refused. Credentials (namespace "snmp", from the credential bundle kind
// snmp_v3 or RETICORA_SNMP_V3_*): username, auth_protocol, auth_password,
// priv_protocol, priv_password and optionally context_name; the
// alternative keys user, authKey and privKey are accepted.

// ErrV3Incomplete refuses an SNMPv3 configuration below authPriv.
var ErrV3Incomplete = errors.New("snmp: SNMPv3 requires authPriv (username, auth and privacy passphrases)")

type v3Config struct {
	user        string
	authProto   gosnmp.SnmpV3AuthProtocol
	authPass    string
	privProto   gosnmp.SnmpV3PrivProtocol
	privPass    string
	contextName string
}

var authProtocols = map[string]gosnmp.SnmpV3AuthProtocol{
	"sha": gosnmp.SHA, "sha1": gosnmp.SHA, "sha224": gosnmp.SHA224, "sha256": gosnmp.SHA256,
	"sha384": gosnmp.SHA384, "sha512": gosnmp.SHA512,
}

var privProtocols = map[string]gosnmp.SnmpV3PrivProtocol{
	"aes": gosnmp.AES, "aes128": gosnmp.AES, "aes192": gosnmp.AES192, "aes256": gosnmp.AES256,
	"aes192c": gosnmp.AES192C, "aes256c": gosnmp.AES256C,
}

func credential(creds map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(creds[k]); v != "" {
			return v
		}
	}
	return ""
}

// v3FromCreds returns the SNMPv3 configuration of creds; ok is false when
// no SNMPv3 user is configured (SNMPv2c is used then).
func v3FromCreds(creds map[string]string) (cfg *v3Config, ok bool, err error) {
	user := credential(creds, "username", "user")
	if user == "" && credential(creds, "version") != "3" {
		return nil, false, nil
	}
	cfg = &v3Config{
		user:        user,
		authPass:    credential(creds, "auth_password", "authKey", "authPassword"),
		privPass:    credential(creds, "priv_password", "privKey", "privPassword"),
		contextName: credential(creds, "context_name", "contextName"),
	}
	if cfg.user == "" || cfg.authPass == "" || cfg.privPass == "" {
		return nil, true, ErrV3Incomplete
	}
	authName := strings.ToLower(credential(creds, "auth_protocol", "authProtocol"))
	if authName == "" {
		authName = "sha256"
	}
	privName := strings.ToLower(credential(creds, "priv_protocol", "privProtocol"))
	if privName == "" {
		privName = "aes"
	}
	var found bool
	if cfg.authProto, found = authProtocols[strings.ReplaceAll(authName, "-", "")]; !found {
		return nil, true, fmt.Errorf("snmp: SNMPv3 auth protocol %q is not allowed (SHA, SHA-224/256/384/512)", authName)
	}
	if cfg.privProto, found = privProtocols[strings.ReplaceAll(privName, "-", "")]; !found {
		return nil, true, fmt.Errorf("snmp: SNMPv3 privacy protocol %q is not allowed (AES, AES-192/256)", privName)
	}
	return cfg, true, nil
}

// session builds an SNMPv3 authPriv session to target.
func (p *Plugin) v3Session(ctx context.Context, target string, cfg *v3Config) *gosnmp.GoSNMP {
	return &gosnmp.GoSNMP{
		Context:       ctx,
		Target:        target,
		Port:          uint16(p.port()),
		Transport:     "udp",
		Version:       gosnmp.Version3,
		Timeout:       p.timeout(),
		Retries:       p.Retries,
		MaxOids:       gosnmp.MaxOids,
		SecurityModel: gosnmp.UserSecurityModel,
		MsgFlags:      gosnmp.AuthPriv,
		ContextName:   cfg.contextName,
		SecurityParameters: &gosnmp.UsmSecurityParameters{
			UserName:                 cfg.user,
			AuthenticationProtocol:   cfg.authProto,
			AuthenticationPassphrase: cfg.authPass,
			PrivacyProtocol:          cfg.privProto,
			PrivacyPassphrase:        cfg.privPass,
		},
	}
}

// v2cSession builds an SNMPv2c session (collection GET and table walks).
func (p *Plugin) v2cSession(ctx context.Context, target, community string) *gosnmp.GoSNMP {
	return &gosnmp.GoSNMP{
		Context: ctx, Target: target, Port: uint16(p.port()), Transport: "udp", Community: community,
		Version: gosnmp.Version2c, Timeout: p.timeout(), Retries: 0, MaxOids: gosnmp.MaxOids,
	}
}

func (p *Plugin) port() int {
	if p.Port > 0 {
		return p.Port
	}
	return defaultPort
}

func (p *Plugin) timeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return defaultTimeout
}

// scalarOIDs are read in one GET.
var scalarOIDs = []string{
	oidSysDescr, oidSysObjectID, oidSysName, oidSysLocation, oidSysContact, oidSysUpTime,
	oidEntPhysSerialNum + ".1", oidEntPhysMfgName + ".1", oidEntPhysModelName + ".1", oidEntPhysFirmwareRev + ".1",
}

// collectSession reads the system group, the Entity-MIB and the interface
// table over an open session.
func (p *Plugin) collectSession(g *gosnmp.GoSNMP, target, version string) (*plugins.Result, error) {
	pkt, err := g.Get(scalarOIDs)
	if err != nil {
		return nil, fmt.Errorf("snmp %s: %w", version, err)
	}
	values := map[string]string{}
	var uptime uint64
	for _, v := range pkt.Variables {
		oid := strings.TrimPrefix(v.Name, ".")
		switch v.Type {
		case gosnmp.OctetString:
			if b, ok := v.Value.([]byte); ok {
				values[oid] = plugins.OctetText(b)
			}
		case gosnmp.ObjectIdentifier:
			if s, ok := v.Value.(string); ok {
				values[oid] = strings.TrimPrefix(s, ".")
			}
		case gosnmp.TimeTicks:
			uptime = gosnmp.ToBigInt(v.Value).Uint64()
		}
	}
	name := values[oidSysName]
	if name == "" {
		name = target
	}
	result := &plugins.Result{
		CIType:       "network.device",
		Name:         name,
		IP:           target,
		Manufacturer: values[oidEntPhysMfgName+".1"],
		Model:        values[oidEntPhysModelName+".1"],
		Serial:       values[oidEntPhysSerialNum+".1"],
		Firmware:     values[oidEntPhysFirmwareRev+".1"],
		Attributes: map[string]any{
			"protocol":    "snmp",
			"sysDescr":    values[oidSysDescr],
			"sysObjectID": values[oidSysObjectID],
			"sysLocation": values[oidSysLocation],
			"sysContact":  values[oidSysContact],
			"snmpVersion": version,
		},
		Interfaces: walkInterfaces(g),
	}
	if uptime > 0 {
		result.Attributes["uptimeSeconds"] = int64(uptime / 100)
	}
	p.applyProfile(result, values[oidSysObjectID])
	return result, nil
}

// walkInterfaces reads the IF-MIB interface table; a device without it
// (or an error) yields no interfaces.
func walkInterfaces(g *gosnmp.GoSNMP) []plugins.NetworkInterface {
	byIndex := map[int]*plugins.NetworkInterface{}
	var order []int
	get := func(idx int) *plugins.NetworkInterface {
		if ifc, ok := byIndex[idx]; ok {
			return ifc
		}
		ifc := &plugins.NetworkInterface{IfIndex: idx}
		byIndex[idx] = ifc
		order = append(order, idx)
		return ifc
	}
	walk := g.BulkWalk
	if g.Version == gosnmp.Version1 {
		walk = g.Walk
	}
	err := walk("1.3.6.1.2.1.2.2.1", func(v gosnmp.SnmpPDU) error {
		oid := strings.TrimPrefix(v.Name, ".")
		column, idx, ok := ifColumn(oid)
		if !ok {
			return nil
		}
		ifc := get(idx)
		switch column {
		case oidIfDescr:
			if b, isBytes := v.Value.([]byte); isBytes {
				ifc.Name = strings.TrimSpace(string(b))
			}
		case oidIfMtu:
			ifc.MTU = int(gosnmp.ToBigInt(v.Value).Int64())
		case oidIfSpeed:
			ifc.Speed = int(gosnmp.ToBigInt(v.Value).Int64() / 1_000_000)
		case oidIfPhysAddr:
			if b, isBytes := v.Value.([]byte); isBytes && len(b) == 6 {
				ifc.MAC = net.HardwareAddr(b).String()
			}
		case oidIfAdminSt:
			ifc.AdminUp = gosnmp.ToBigInt(v.Value).Int64() == 1
		case oidIfOperSt:
			ifc.OperUp = gosnmp.ToBigInt(v.Value).Int64() == 1
		}
		return nil
	})
	if err != nil && len(order) == 0 {
		return nil
	}
	out := make([]plugins.NetworkInterface, 0, len(order))
	for _, idx := range order {
		ifc := byIndex[idx]
		if ifc.Name == "" {
			ifc.Name = "if" + strconv.Itoa(idx)
		}
		out = append(out, *ifc)
	}
	return out
}

// ifColumn splits an ifTable OID into its column OID and ifIndex.
func ifColumn(oid string) (column string, idx int, ok bool) {
	i := strings.LastIndexByte(oid, '.')
	if i < 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(oid[i+1:])
	if err != nil {
		return "", 0, false
	}
	return oid[:i], n, true
}

// probeV3 checks one target with an authenticated, encrypted GET of
// sysObjectID.
func (p *Plugin) probeV3(ctx context.Context, target string, cfg *v3Config) (*plugins.Result, bool) {
	g := p.v3Session(ctx, target, cfg)
	if err := g.Connect(); err != nil {
		return nil, false
	}
	defer g.Conn.Close()
	pkt, err := g.Get([]string{oidSysObjectID})
	if err != nil || len(pkt.Variables) == 0 || pkt.Variables[0].Type == gosnmp.NoSuchObject {
		return nil, false
	}
	return &plugins.Result{
		CIType: "network.device",
		Name:   target,
		IP:     target,
		Attributes: map[string]any{
			"protocol":      "snmp",
			"snmpReachable": true,
			"snmpVersion":   "v3",
		},
	}, true
}

// collectV3 collects target and its neighbours over SNMPv3 authPriv.
func (p *Plugin) collectV3(ctx context.Context, target string, cfg *v3Config) (*plugins.Result, []plugins.DeviceNeighbor, error) {
	g := p.v3Session(ctx, target, cfg)
	if err := g.Connect(); err != nil {
		return nil, nil, fmt.Errorf("snmp v3: connect %s: %w", target, err)
	}
	defer g.Conn.Close()
	result, err := p.collectSession(g, target, "v3")
	if err != nil {
		return nil, nil, err
	}
	return result, walkNeighbors(g, target, result.Interfaces), nil
}
