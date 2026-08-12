// Package profiles manages vendor-specific discovery and collection profiles.
package profiles

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

// embeddedData holds the vendor profiles shipped with the collector binary
// (JSON documents in data/), so discovery works without an external profile
// directory. Operators can still load additional profiles from disk.
//
//go:embed data
var embeddedData embed.FS

// Profile describes vendor-specific mappings and commands used by collector plugins.
type Profile struct {
	Name        string            `json:"name"`
	Vendor      string            `json:"vendor,omitempty"`
	Model       string            `json:"model,omitempty"`
	Protocol    string            `json:"protocol,omitempty"`
	SNMPOIDs    map[string]string `json:"snmpOids,omitempty"`
	SSHCommands map[string]string `json:"sshCommands,omitempty"`
	Endpoints   map[string]string `json:"endpoints,omitempty"`
	Attributes  map[string]any    `json:"attributes,omitempty"`

	// SysObjectIDs lists SNMP sysObjectID values (1.3.6.1.2.1.1.2.0) that
	// identify this device family. Matching uses longest-prefix, so a profile
	// registered for "1.3.6.1.4.1.9.1" covers every Cisco model OID below it.
	SysObjectIDs []string `json:"sysObjectIds,omitempty"`

	// OUIPrefixes lists MAC address OUI prefixes (first 3 octets, hex, any
	// separator) identifying the vendor; used when only a MAC is known.
	OUIPrefixes []string `json:"ouiPrefixes,omitempty"`
}

// Registry stores known vendor profiles in memory.
type Registry struct {
	mu       sync.RWMutex
	profiles map[string]Profile
}

// NewRegistry returns an empty profile registry.
func NewRegistry() *Registry {
	return &Registry{profiles: make(map[string]Profile)}
}

// LoadEmbedded registers every profile shipped inside the binary (data/*.json).
func (r *Registry) LoadEmbedded() error {
	return fs.WalkDir(embeddedData, "data", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		f, err := embeddedData.Open(path)
		if err != nil {
			return fmt.Errorf("profiles: open embedded %s: %w", path, err)
		}
		defer f.Close()
		if err := r.LoadJSON(f); err != nil {
			return fmt.Errorf("profiles: load embedded %s: %w", path, err)
		}
		return nil
	})
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
