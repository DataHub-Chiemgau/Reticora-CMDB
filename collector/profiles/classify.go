package profiles

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// Classification (DIS-02): one chain for every DeviceRecord, in the order
// SNMP → Redfish → Banner. The first stage with a matching profile decides
// type, vendor, model, plugin and system template. Without a match the
// device is a generic_device classified by "fallback" (the server opens an
// unclassified_device review); the MAC OUI still names its vendor. The
// sweep record of a device another protocol classified is folded into that
// record before the upload (MergeByTarget).

// GenericDevice is the CI type of devices no profile matched.
const GenericDevice = "generic_device"

// SystemCITypes are the system CI types a profile may assign (seeded by the
// server, migration 000021).
var SystemCITypes = map[string]bool{
	"switch": true, "router": true, "firewall": true, "access_point": true, "server": true,
	"hypervisor": true, "vm": true, "client": true, "pdu": true, "ups": true, "nas": true,
	"storage_array": true, "printer": true, "ip_phone": true, "camera": true,
	GenericDevice: true, "patch_panel": true,
}

// Classification stages, in order.
const (
	StageSNMP     = "snmp"
	StageRedfish  = "redfish"
	StageBanner   = "banner"
	StageFallback = "fallback"
)

// Observation is what discovery learned about a device.
type Observation struct {
	SysObjectID string
	SysDescr    string
	// Redfish is "manufacturer model" reported over Redfish.
	Redfish string
	// Banners are banners and self-descriptions (SSH banner, uname, OS
	// name, NAS/UPS identification, manufacturer and model).
	Banners []string
	MAC     string
}

// Result is the outcome of the classification chain.
type Result struct {
	Stage   string
	Profile *Profile
	// Vendor is the OUI vendor when no profile matched.
	Vendor string
}

// Classified reports whether a profile matched.
func (r Result) Classified() bool { return r.Profile != nil }

type compiledProfile struct {
	profile  Profile
	sysDescr *regexp.Regexp
	redfish  *regexp.Regexp
	banner   *regexp.Regexp
}

// Validate checks a profile: a classifying profile needs a system CI type
// and compilable patterns.
func (p *Profile) Validate() error {
	if p.Name == "" {
		return fmt.Errorf("profiles: name is required")
	}
	if p.CIType == "" {
		if p.SysDescrPattern != "" || p.RedfishPattern != "" || p.BannerPattern != "" {
			return fmt.Errorf("profiles: %s has match patterns but no ciType", p.Name)
		}
		return nil
	}
	if !SystemCITypes[p.CIType] {
		return fmt.Errorf("profiles: %s: unknown ciType %q", p.Name, p.CIType)
	}
	_, err := compile(p)
	return err
}

func compile(p *Profile) (*compiledProfile, error) {
	c := &compiledProfile{profile: *p}
	for _, f := range []struct {
		pattern string
		dst     **regexp.Regexp
		field   string
	}{{p.SysDescrPattern, &c.sysDescr, "sysDescrPattern"}, {p.RedfishPattern, &c.redfish, "redfishPattern"}, {p.BannerPattern, &c.banner, "bannerPattern"}} {
		if f.pattern == "" {
			continue
		}
		re, err := regexp.Compile(f.pattern)
		if err != nil {
			return nil, fmt.Errorf("profiles: %s: %s: %w", p.Name, f.field, err)
		}
		*f.dst = re
	}
	return c, nil
}

// classifiers returns the compiled classifying profiles in name order.
func (r *Registry) classifiers() []*compiledProfile {
	r.mu.RLock()
	if r.compiled != nil {
		defer r.mu.RUnlock()
		return r.compiled
	}
	r.mu.RUnlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.compiled != nil {
		return r.compiled
	}
	out := []*compiledProfile{}
	for name := range r.profiles {
		p := r.profiles[name]
		if p.CIType == "" {
			continue
		}
		c, err := compile(&p)
		if err != nil {
			continue // Validate refused it on load; registered by hand
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].profile.Name < out[j].profile.Name })
	r.compiled = out
	return out
}

// Classify runs the chain SNMP → Redfish → Banner over the observation.
func (r *Registry) Classify(o *Observation) Result {
	cs := r.classifiers()
	if p := classifySNMP(cs, o); p != nil {
		return Result{Stage: StageSNMP, Profile: p}
	}
	if p := classifyRedfish(cs, o); p != nil {
		return Result{Stage: StageRedfish, Profile: p}
	}
	if p := classifyBanner(cs, o); p != nil {
		return Result{Stage: StageBanner, Profile: p}
	}
	vendor, _ := r.VendorByMAC(o.MAC)
	return Result{Stage: StageFallback, Vendor: vendor}
}

// classifySNMP: a profile is a candidate when its sysObjectID prefix matches
// or (without sysObjectIDs) its sysDescr pattern matches; a sysDescr
// pattern that does not match excludes it. A matching sysDescr pattern
// beats a longer OID prefix, so a model profile wins over the vendor
// profile and over generic OS profiles.
func classifySNMP(cs []*compiledProfile, o *Observation) *Profile {
	oid, descr := strings.TrimSpace(o.SysObjectID), o.SysDescr
	if oid == "" && descr == "" {
		return nil
	}
	var best *Profile
	bestScore := -1
	for _, c := range cs {
		prefix := 0
		for _, candidate := range c.profile.SysObjectIDs {
			candidate = strings.TrimSpace(candidate)
			if candidate != "" && oid != "" && oidMatches(candidate, oid) && len(candidate) > prefix {
				prefix = len(candidate)
			}
		}
		score := prefix
		switch {
		case c.sysDescr != nil && descr != "" && c.sysDescr.MatchString(descr):
			score += 1000
		case c.sysDescr != nil:
			continue
		case prefix == 0:
			continue
		}
		if score > bestScore {
			best, bestScore = &c.profile, score
		}
	}
	return best
}

func classifyRedfish(cs []*compiledProfile, o *Observation) *Profile {
	if strings.TrimSpace(o.Redfish) == "" {
		return nil
	}
	for _, c := range cs {
		if c.redfish != nil && c.redfish.MatchString(o.Redfish) {
			return &c.profile
		}
	}
	return nil
}

func classifyBanner(cs []*compiledProfile, o *Observation) *Profile {
	text := strings.Join(o.Banners, " | ")
	if strings.TrimSpace(strings.ReplaceAll(text, "|", "")) == "" {
		return nil
	}
	var best *Profile
	for _, c := range cs {
		if c.banner == nil || !c.banner.MatchString(text) {
			continue
		}
		if best == nil || c.profile.Priority > best.Priority {
			best = &c.profile
		}
	}
	return best
}

// bannerAttributes are the record attributes that describe the device.
var bannerAttributes = []string{"sshBanner", "uname", "os_release", "osFamily", "osName", "vendor", "nasDevice", "bmcVendor", "bmcProduct", "deviceType"}

// ObservationOf collects the evidence of a DeviceRecord.
func ObservationOf(rec *plugins.DeviceRecord) Observation {
	o := Observation{
		SysObjectID: rec.Classification.SysObjectID,
		SysDescr:    rec.System.Description,
		MAC:         rec.Identity.PrimaryMAC,
	}
	vendorModel := strings.TrimSpace(rec.Classification.Manufacturer + " " + rec.Classification.Model)
	if rec.Source == "redfish" {
		o.Redfish = strings.TrimSpace(vendorModel + " " + attrString(rec.Attributes, "bmcVendor") + " " + attrString(rec.Attributes, "bmcProduct"))
	}
	for _, v := range []string{vendorModel, rec.System.OSName, rec.System.Description} {
		if v != "" {
			o.Banners = append(o.Banners, v)
		}
	}
	for _, key := range bannerAttributes {
		if v := attrString(rec.Attributes, key); v != "" {
			o.Banners = append(o.Banners, v)
		}
	}
	return o
}

func attrString(attrs map[string]any, key string) string {
	if v, ok := attrs[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// ClassifyRecord classifies rec in place: type, vendor and model (where the
// plugin knew none), profile, and the attributes classified_by, plugin and
// system_template; an unmatched record becomes generic_device with
// classified_by "fallback", for which the server opens an
// unclassified_device review.
func (r *Registry) ClassifyRecord(rec *plugins.DeviceRecord) Result {
	o := ObservationOf(rec)
	res := r.Classify(&o)
	if rec.Attributes == nil {
		rec.Attributes = map[string]any{}
	}
	rec.Attributes["classified_by"] = res.Stage
	if !res.Classified() {
		rec.Classification.CIType = GenericDevice
		rec.Classification.Profile = ""
		if rec.Classification.Manufacturer == "" {
			rec.Classification.Manufacturer = res.Vendor
		}
		return res
	}
	p := res.Profile
	rec.Classification.CIType = p.CIType
	rec.Classification.Profile = p.Name
	if rec.Classification.Manufacturer == "" {
		rec.Classification.Manufacturer = p.Vendor
	}
	if rec.Classification.Model == "" {
		rec.Classification.Model = p.Model
	}
	for key, value := range map[string]string{"plugin": p.Plugin, "system_template": p.SystemTemplate} {
		if value != "" {
			rec.Attributes[key] = value
		}
	}
	return res
}

// MergeByTarget merges the classified records of one discovery run per
// scope and target before the upload (DIS-02): the chain classifies a
// device, and the sweep only tells that the target answered. The sweep
// record of a target that a profile classified through another protocol of
// the same scope is folded into that protocol's record – the sweep's MAC,
// hostname and FQDN fill what that record lacks – and is not uploaded on its
// own; the server would take it for an unclassified device. Only for a
// target no protocol classified does the sweep record stay, as
// generic_device with classified_by "fallback". The records of the other
// protocols are kept as they are.
func MergeByTarget(recs []plugins.DeviceRecord) []plugins.DeviceRecord {
	classified := map[string]int{}
	for i := range recs {
		if key := targetKey(&recs[i]); key != "" && profileMatched(&recs[i]) {
			if _, ok := classified[key]; !ok {
				classified[key] = i
			}
		}
	}
	if len(classified) == 0 {
		return recs
	}
	folded := make([]bool, len(recs))
	for i := range recs {
		into, ok := classified[targetKey(&recs[i])]
		if !ok || recs[i].Source != "sweep" || profileMatched(&recs[i]) {
			continue
		}
		id, sweep := &recs[into].Identity, recs[i].Identity
		for _, f := range []struct {
			dst *string
			src string
		}{{&id.PrimaryMAC, sweep.PrimaryMAC}, {&id.Hostname, sweep.Hostname}, {&id.FQDN, sweep.FQDN}} {
			if *f.dst == "" {
				*f.dst = f.src
			}
		}
		folded[i] = true
	}
	out := make([]plugins.DeviceRecord, 0, len(recs))
	for i := range recs {
		if !folded[i] {
			out = append(out, recs[i])
		}
	}
	return out
}

// targetKey is the target of a record within its scope: the address, or
// the name of a target configured by name that has no address yet.
func targetKey(rec *plugins.DeviceRecord) string {
	target := rec.TargetIP
	if target == "" {
		target = rec.Identity.Hostname
	}
	if target == "" {
		return ""
	}
	return rec.ScopeID + "/" + target
}

// profileMatched reports whether the chain classified rec with a profile.
func profileMatched(rec *plugins.DeviceRecord) bool {
	by, _ := rec.Attributes["classified_by"].(string)
	return by != "" && by != StageFallback
}

var (
	defaultOnce     sync.Once
	defaultRegistry *Registry
)

// Default returns the registry of the shipped profiles.
func Default() *Registry {
	defaultOnce.Do(func() {
		defaultRegistry = NewRegistry()
		if err := defaultRegistry.LoadEmbedded(); err != nil {
			panic(err) // TestEmbeddedProfileData keeps the shipped data valid
		}
	})
	return defaultRegistry
}
