// Package profiles manages vendor-specific discovery and collection profiles.
package profiles

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// embeddedData holds the profiles shipped with the collector binary (YAML
// documents in data/), so discovery works without an external profile
// directory. Operators load additional or updated profiles from a directory
// (LoadDir, RETICORA_PROFILES_DIR); a profile with the same name replaces
// the shipped one.
//
//go:embed data
var embeddedData embed.FS

// Profile describes a device family (DIS-02): how it is recognised (SNMP
// sysObjectID/sysDescr, Redfish manufacturer/model, banners, MAC OUI), what
// it is (CI type, vendor, model, plugin, system template) and the
// vendor-specific collection data.
type Profile struct {
	Name     string `json:"name" yaml:"name"`
	Vendor   string `json:"vendor,omitempty" yaml:"vendor,omitempty"`
	Model    string `json:"model,omitempty" yaml:"model,omitempty"`
	Protocol string `json:"protocol,omitempty" yaml:"protocol,omitempty"`
	// Catalog names the entry of the CH30 profile list the profile covers.
	Catalog string `json:"catalog,omitempty" yaml:"catalog,omitempty"`

	// CIType is the system CI type a matching device gets; profiles without
	// it carry collection data only and never classify.
	CIType string `json:"ciType,omitempty" yaml:"ciType,omitempty"`
	// Plugin is the protocol plugin that collects the device in depth.
	Plugin string `json:"plugin,omitempty" yaml:"plugin,omitempty"`
	// SystemTemplate names the attribute template of the device's CI.
	SystemTemplate string `json:"systemTemplate,omitempty" yaml:"systemTemplate,omitempty"`
	// Priority orders profiles whose banner patterns both match (higher
	// wins); generic profiles use a negative value.
	Priority int `json:"priority,omitempty" yaml:"priority,omitempty"`

	SNMPOIDs    map[string]string `json:"snmpOids,omitempty" yaml:"snmpOids,omitempty"`
	SSHCommands map[string]string `json:"sshCommands,omitempty" yaml:"sshCommands,omitempty"`
	Endpoints   map[string]string `json:"endpoints,omitempty" yaml:"endpoints,omitempty"`
	Attributes  map[string]any    `json:"attributes,omitempty" yaml:"attributes,omitempty"`

	// SysObjectIDs lists SNMP sysObjectID values (1.3.6.1.2.1.1.2.0) that
	// identify this device family. Matching uses longest-prefix, so a profile
	// registered for "1.3.6.1.4.1.9.1" covers every Cisco model OID below it.
	SysObjectIDs []string `json:"sysObjectIds,omitempty" yaml:"sysObjectIds,omitempty"`
	// SysDescrPattern (regular expression) must match the SNMP sysDescr;
	// it distinguishes models below one sysObjectID.
	SysDescrPattern string `json:"sysDescrPattern,omitempty" yaml:"sysDescrPattern,omitempty"`
	// RedfishPattern matches "manufacturer model" reported over Redfish.
	RedfishPattern string `json:"redfishPattern,omitempty" yaml:"redfishPattern,omitempty"`
	// BannerPattern matches the banners and self-descriptions of the
	// device (SSH banner, uname, OS name, NAS/UPS identification).
	BannerPattern string `json:"bannerPattern,omitempty" yaml:"bannerPattern,omitempty"`

	// OUIPrefixes lists MAC address OUI prefixes (first 3 octets, hex, any
	// separator) identifying the vendor; used when only a MAC is known.
	OUIPrefixes []string `json:"ouiPrefixes,omitempty" yaml:"ouiPrefixes,omitempty"`
}

// Registry stores known vendor profiles in memory.
type Registry struct {
	mu       sync.RWMutex
	profiles map[string]Profile
	// compiled caches the classifying profiles with their patterns;
	// Register resets it.
	compiled []*compiledProfile
}

// NewRegistry returns an empty profile registry.
func NewRegistry() *Registry {
	return &Registry{profiles: make(map[string]Profile)}
}

// LoadEmbedded registers every profile shipped inside the binary (data/*.yaml).
func (r *Registry) LoadEmbedded() error {
	return fs.WalkDir(embeddedData, "data", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !isYAML(entry.Name()) {
			return nil
		}
		f, err := embeddedData.Open(path)
		if err != nil {
			return fmt.Errorf("profiles: open embedded %s: %w", path, err)
		}
		defer f.Close()
		if err := r.LoadYAML(f); err != nil {
			return fmt.Errorf("profiles: load embedded %s: %w", path, err)
		}
		return nil
	})
}

// LoadDir registers the YAML profiles of dir (*.yaml, *.yml); they add to
// or replace the shipped profiles, so profiles and OUI prefixes are updated
// without a new collector release.
func (r *Registry) LoadDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("profiles: read %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("profiles: open %s: %w", path, err)
		}
		err = r.LoadYAML(f)
		f.Close()
		if err != nil {
			return fmt.Errorf("profiles: load %s: %w", path, err)
		}
	}
	return nil
}

func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

// LoadYAML registers the profiles of a YAML list. Each profile is
// validated: classifying profiles need a known CI type and valid patterns.
func (r *Registry) LoadYAML(reader io.Reader) error {
	var list []Profile
	dec := yaml.NewDecoder(reader)
	dec.KnownFields(true)
	if err := dec.Decode(&list); err != nil {
		return fmt.Errorf("profiles: decode YAML: %w", err)
	}
	for i := range list {
		if err := list[i].Validate(); err != nil {
			return err
		}
		if err := r.Register(list[i]); err != nil {
			return err
		}
	}
	return nil
}

// Register adds or replaces a profile in the registry.
func (r *Registry) Register(profile Profile) error {
	if profile.Name == "" {
		return fmt.Errorf("profiles: name is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.profiles == nil {
		r.profiles = make(map[string]Profile)
	}
	r.profiles[profile.Name] = profile
	r.compiled = nil
	return nil
}

// Get returns a profile by name.
func (r *Registry) Get(name string) (Profile, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	profile, ok := r.profiles[name]
	return profile, ok
}

// BySysObjectID resolves a profile from an SNMP sysObjectID. The profile with
// the longest matching OID prefix wins, so specific model profiles beat
// generic vendor profiles. Returns false when nothing matches.
func (r *Registry) BySysObjectID(sysObjectID string) (Profile, bool) {
	sysObjectID = strings.TrimSpace(sysObjectID)
	if sysObjectID == "" {
		return Profile{}, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	var best Profile
	bestLen := -1
	for _, profile := range r.profiles {
		for _, oid := range profile.SysObjectIDs {
			oid = strings.TrimSpace(oid)
			if oid == "" {
				continue
			}
			if oidMatches(oid, sysObjectID) && len(oid) > bestLen {
				best = profile
				bestLen = len(oid)
			}
		}
	}
	return best, bestLen >= 0
}

// oidMatches reports whether candidate is the registered prefix itself or a
// descendant of it (dot-boundary aware, so 1.3.6.1.4.1.9.1 does not match
// 1.3.6.1.4.1.9.10 by string prefix alone).
func oidMatches(prefix, candidate string) bool {
	if candidate == prefix {
		return true
	}
	return strings.HasPrefix(candidate, prefix+".")
}

// VendorByMAC resolves the vendor name for a MAC address via the OUI prefix
// (first 3 octets). Separators (":", "-", ".") and case are ignored. Returns
// false when no profile declares a matching OUI.
func (r *Registry) VendorByMAC(mac string) (string, bool) {
	oui := normalizeOUI(mac)
	if oui == "" {
		return "", false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, profile := range r.profiles {
		if profile.Vendor == "" {
			continue
		}
		for _, prefix := range profile.OUIPrefixes {
			if normalizeOUI(prefix) == oui {
				return profile.Vendor, true
			}
		}
	}
	return "", false
}

// normalizeOUI reduces a MAC address or OUI prefix to 6 lowercase hex digits.
func normalizeOUI(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer(":", "", "-", "", ".", "").Replace(value)
	if len(value) < 6 {
		return ""
	}
	value = value[:6]
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return value
}

// List returns all profiles in deterministic order.
func (r *Registry) List() []Profile {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.profiles))
	for name := range r.profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]Profile, 0, len(names))
	for _, name := range names {
		out = append(out, r.profiles[name])
	}
	return out
}

// LoadJSON registers profiles from a JSON array or object map.
func (r *Registry) LoadJSON(reader io.Reader) error {
	payload, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("profiles: read payload: %w", err)
	}

	var list []Profile
	if err := json.Unmarshal(payload, &list); err == nil {
		for _, profile := range list {
			if err := r.Register(profile); err != nil {
				return err
			}
		}
		return nil
	}

	var named map[string]Profile
	if err := json.Unmarshal(payload, &named); err != nil {
		return fmt.Errorf("profiles: decode JSON: %w", err)
	}
	for name, profile := range named {
		if profile.Name == "" {
			profile.Name = name
		}
		if err := r.Register(profile); err != nil {
			return err
		}
	}
	return nil
}
