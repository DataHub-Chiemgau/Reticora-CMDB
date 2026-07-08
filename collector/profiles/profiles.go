// Package profiles manages vendor-specific discovery and collection profiles.
package profiles

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"sync"
)

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
