package config

import (
	"regexp"
	"strings"
)

// Platform is a device platform whose running configuration the collector
// reads (WP-1053 AK1). The set is fixed; another platform is
// unsupported_platform.
type Platform string

// The supported platforms.
const (
	CiscoIOS  Platform = "cisco_ios" // IOS and IOS-XE
	CiscoNXOS Platform = "cisco_nxos"
	Junos     Platform = "junos"
	RouterOS  Platform = "routeros" //nolint:misspell // platform key of WP-1053 (MikroTik RouterOS)
	HuaweiVRP Platform = "huawei_vrp"
)

// Platforms lists every supported platform.
var Platforms = []Platform{CiscoIOS, CiscoNXOS, Junos, RouterOS, HuaweiVRP}

// Valid reports whether p is a supported platform.
func (p Platform) Valid() bool {
	for _, x := range Platforms {
		if p == x {
			return true
		}
	}
	return false
}

// The commands of the detection and the read; they are the
// ConfigCommands of collector/plugins/ssh and fixed in the code.
const (
	cmdShowVersion = "show version"
	cmdCiscoConfig = "show running-config"
	cmdJunosConfig = "show configuration | display set | no-more"
	cmdRouterOS    = "/export terse"
	cmdHuawei      = "display current-configuration"
)

// Command returns the command that reads the running configuration.
func (p Platform) Command() string {
	switch p {
	case CiscoIOS, CiscoNXOS:
		return cmdCiscoConfig
	case Junos:
		return cmdJunosConfig
	case RouterOS:
		return cmdRouterOS
	case HuaweiVRP:
		return cmdHuawei
	}
	return ""
}

// PlatformFromBanner detects the platform from the identification string
// of the SSH server: ROSSSH is RouterOS, HUAWEI Huawei VRP. Others need
// show version.
func PlatformFromBanner(serverVersion string) (Platform, bool) {
	v := strings.ToUpper(serverVersion)
	switch {
	case strings.Contains(v, "ROSSSH"):
		return RouterOS, true
	case strings.Contains(v, "HUAWEI"):
		return HuaweiVRP, true
	}
	return "", false
}

// PlatformFromVersion detects the platform from the output of show
// version: NX-OS before IOS (an NX-OS output names Cisco as well), then
// JUNOS.
func PlatformFromVersion(output string) (Platform, bool) {
	switch {
	case strings.Contains(output, "NX-OS"):
		return CiscoNXOS, true
	case strings.Contains(output, "Cisco IOS"):
		return CiscoIOS, true
	case strings.Contains(strings.ToUpper(output), "JUNOS"):
		return Junos, true
	}
	return "", false
}

// volatileLines are the lines per platform that change without a change of
// the configuration (time stamps, build banners); normalization removes
// them before masking and hashing (WP-1053 AK3).
var volatileLines = map[Platform][]*regexp.Regexp{
	CiscoIOS: {
		regexp.MustCompile(`^! Last configuration change at`),
		regexp.MustCompile(`^! NVRAM config last updated`),
		regexp.MustCompile(`^! No configuration change since last restart`),
		regexp.MustCompile(`^Building configuration\.{3}`),
		regexp.MustCompile(`^Current configuration :`),
		regexp.MustCompile(`^ntp clock-period `),
	},
	CiscoNXOS: {
		regexp.MustCompile(`^!Time:`),
		regexp.MustCompile(`^!Command: show running-config`),
		regexp.MustCompile(`^!Running configuration last done at:`),
	},
	Junos: {
		regexp.MustCompile(`^## Last commit:`),
		regexp.MustCompile(`^## Last changed:`),
	},
	RouterOS: {
		regexp.MustCompile(`^# .* by RouterOS`),
	},
	HuaweiVRP: {
		regexp.MustCompile(`^!Last configuration was (updated|saved) at`),
		regexp.MustCompile(`^!Time:`),
	},
}
