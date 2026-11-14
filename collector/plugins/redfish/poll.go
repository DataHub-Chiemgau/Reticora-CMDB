package redfish

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// Health polling of a Redfish service for the probe type redfish (MON-03,
// WP-998): system health and power state, temperatures, fans and power
// supplies of the chassis. Only GET requests are sent; credentials go only
// to targets whose certificate the system roots or a TLS scope vouch for
// (COL-02), as in the discovery.

// Limits of a poll (WP-998 AK2).
const (
	// DefaultMaxRequests bounds the resource requests of one poll.
	DefaultMaxRequests = 20
	// MaxBodyBytes bounds the body of one resource.
	MaxBodyBytes = 1 << 20
)

// ErrBodyTooLarge is returned when a resource exceeds MaxBodyBytes; the
// resource is discarded, never decoded in part.
var ErrBodyTooLarge = errors.New("redfish: response body exceeds 1 MiB")

// ErrInvalidOption is returned for a system or chassis Id of the options
// that is no plain path segment.
var ErrInvalidOption = errors.New("redfish: invalid resource id")

// ErrForeignLink is returned for a resource link of the device that is no
// path on the target (an absolute URL, "//host/x"): the device, not the
// options, is at fault.
var ErrForeignLink = errors.New("redfish: resource link leaves the target")

// ValidID reports whether id is a plain Redfish resource Id (letters,
// digits, '.', '_', '-', at most 64 characters, not "." or ".."), which
// PollOptions.SystemID and ChassisIDs must be.
func ValidID(id string) bool {
	if id == "" || len(id) > 64 || id == "." || id == ".." {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// ErrNoSystem is returned when the service lists no computer system and no
// chassis, so there is nothing to judge.
var ErrNoSystem = errors.New("redfish: no system or chassis")

// PollOptions select the resources of a poll.
type PollOptions struct {
	// Port of the Redfish service; 0 uses the port of the plugin.
	Port int
	// SystemID is the Id of the computer system; empty reads the first
	// member of the Systems collection.
	SystemID string
	// ChassisIDs are the Ids of the chassis to read; empty reads every
	// member of the Chassis collection (within MaxRequests).
	ChassisIDs []string
	// MaxRequests bounds the resource requests (≤ 0: DefaultMaxRequests).
	MaxRequests int
}

// HealthState is a Redfish Status.Health value mapped to a number: OK 0,
// Warning 1, Critical 2. An absent or unknown health is OK.
type HealthState int

// Health states.
const (
	HealthOK HealthState = iota
	HealthWarning
	HealthCritical
)

// parseHealth maps Status.Health.
func parseHealth(s string) HealthState {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "warning":
		return HealthWarning
	case "critical":
		return HealthCritical
	}
	return HealthOK
}

// Temperature is one temperature sensor.
type Temperature struct {
	Name    string
	Celsius float64
	// UpperNonCritical and UpperCritical are the thresholds of the device
	// (nil: the device reports none).
	UpperNonCritical *float64
	UpperCritical    *float64
	Health           HealthState
}

// Fan is one fan.
type Fan struct {
	Name   string
	RPM    *float64
	Health HealthState
}

// PowerSupply is one power supply.
type PowerSupply struct {
	Name       string
	InputWatts *float64
	Health     HealthState
}

// Health is the result of a poll.
type Health struct {
	// Health is the worst Status.Health of the system and the chassis.
	Health        HealthState
	PowerState    string
	Temperatures  []Temperature
	Fans          []Fan
	PowerSupplies []PowerSupply
	// Truncated reports that MaxRequests ended the poll before every
	// resource was read.
	Truncated bool
	// Requests is the number of resource requests sent.
	Requests int
}

// status is the Status object of a Redfish resource.
type status struct {
	State  string `json:"State"`
	Health string `json:"Health"`
}

type chassis struct {
	ID               string `json:"Id"`
	Status           status `json:"Status"`
	Thermal          link   `json:"Thermal"`
	Power            link   `json:"Power"`
	ThermalSubsystem link   `json:"ThermalSubsystem"`
	PowerSubsystem   link   `json:"PowerSubsystem"`
}

// Legacy Thermal and Power resources (deprecated in Redfish 2020.4, still
// served by most BMCs).
type legacyThermal struct {
	Temperatures []struct {
		Name                      string   `json:"Name"`
		ReadingCelsius            *float64 `json:"ReadingCelsius"`
		UpperThresholdNonCritical *float64 `json:"UpperThresholdNonCritical"`
		UpperThresholdCritical    *float64 `json:"UpperThresholdCritical"`
		Status                    status   `json:"Status"`
	} `json:"Temperatures"`
	Fans []struct {
		Name         string   `json:"Name"`
		FanName      string   `json:"FanName"`
		Reading      *float64 `json:"Reading"`
		ReadingUnits string   `json:"ReadingUnits"`
		Status       status   `json:"Status"`
	} `json:"Fans"`
}

type legacyPower struct {
	PowerSupplies []struct {
		Name            string   `json:"Name"`
		MemberID        string   `json:"MemberId"`
		PowerInputWatts *float64 `json:"PowerInputWatts"`
		Status          status   `json:"Status"`
	} `json:"PowerSupplies"`
}

// Subsystem resources (Redfish 2020.4 and later).
type thermalSubsystem struct {
	ThermalMetrics link `json:"ThermalMetrics"`
	Fans           link `json:"Fans"`
}

type thermalMetrics struct {
	TemperatureReadingsCelsius []struct {
		DeviceName    string   `json:"DeviceName"`
		DataSourceURI string   `json:"DataSourceUri"`
		Reading       *float64 `json:"Reading"`
	} `json:"TemperatureReadingsCelsius"`
}

type fanResource struct {
	ID           string `json:"Id"`
	Name         string `json:"Name"`
	Status       status `json:"Status"`
	SpeedPercent struct {
		SpeedRPM *float64 `json:"SpeedRPM"`
	} `json:"SpeedPercent"`
}

type powerSubsystem struct {
	PowerSupplies link `json:"PowerSupplies"`
}

type powerSupplyResource struct {
	ID      string `json:"Id"`
	Name    string `json:"Name"`
	Status  status `json:"Status"`
	Metrics link   `json:"Metrics"`
}

type powerSupplyMetrics struct {
	InputPowerWatts struct {
		Reading *float64 `json:"Reading"`
	} `json:"InputPowerWatts"`
}

// errRequestLimit ends a poll at MaxRequests.
var errRequestLimit = errors.New("redfish: request limit reached")

// poller is one poll: the request budget and the target.
type poller struct {
	p        *Plugin
	client   *http.Client
	base     string
	username string
	password string
	max      int
	n        int
}

// get fetches one resource within the budget. Only an absolute path on
// the target is followed: a link of the device such as "@host/x" would
// otherwise move the request, and the credential, to another host.
func (q *poller) get(ctx context.Context, path string, out any) error {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return fmt.Errorf("%w: link %q is no path on the target", ErrForeignLink, path)
	}
	if q.n >= q.max {
		return errRequestLimit
	}
	q.n++
	return q.p.redfishGet(ctx, q.client, q.base+path, q.username, q.password, out)
}

// PollHealth reads the health of target (WP-998 AK1): Systems/{id}
// (Status.Health, PowerState) and per chassis either ThermalSubsystem
// (ThermalMetrics, Fans) and PowerSubsystem/PowerSupplies or the older
// Thermal and Power resources. creds holds username and password of the
// namespace redfish (Basic authentication).
//
// A refused credential (401, 403) is plugins.ErrAuthFailed, a certificate
// neither the system roots nor a TLS scope vouch for is a failed
// certificate verification (plugins.UntrustedCertificate) and no request
// reached the target; a resource above MaxBodyBytes is ErrBodyTooLarge.
// MaxRequests ends the poll with the resources read so far and
// Health.Truncated.
func (p *Plugin) PollHealth(ctx context.Context, target string, creds map[string]string, opts PollOptions) (*Health, error) {
	if opts.SystemID != "" && !ValidID(opts.SystemID) {
		return nil, ErrInvalidOption
	}
	for _, id := range opts.ChassisIDs {
		if !ValidID(id) {
			return nil, ErrInvalidOption
		}
	}
	port := opts.Port
	if port == 0 {
		port = p.port
	}
	if port == 0 {
		port = defaultPort
	}
	q := &poller{
		p:        p,
		client:   p.clientFor(target),
		base:     "https://" + net.JoinHostPort(target, strconv.Itoa(port)),
		username: creds["username"],
		password: creds["password"],
		max:      opts.MaxRequests,
	}
	if q.max <= 0 {
		q.max = DefaultMaxRequests
	}
	h := &Health{}
	err := q.poll(ctx, h, opts)
	h.Requests = q.n
	if errors.Is(err, errRequestLimit) {
		h.Truncated = true
		err = nil
	}
	if err != nil {
		return nil, err
	}
	return h, nil
}

func (q *poller) poll(ctx context.Context, h *Health, opts PollOptions) error {
	var root serviceRoot
	if err := q.get(ctx, serviceRootPath, &root); err != nil {
		return fmt.Errorf("redfish: service root: %w", err)
	}
	found := false
	if sysPath, err := q.systemPath(ctx, &root, opts.SystemID); err != nil {
		return err
	} else if sysPath != "" {
		var sys computerSystem
		if err := q.get(ctx, sysPath, &sys); err != nil {
			return fmt.Errorf("redfish: system: %w", err)
		}
		found = true
		h.Health = max(h.Health, parseHealth(sys.Status.Health))
		h.PowerState = sys.PowerState
	}
	paths, err := q.chassisPaths(ctx, &root, opts.ChassisIDs)
	if err != nil {
		return err
	}
	for _, path := range paths {
		var c chassis
		if err := q.get(ctx, path, &c); err != nil {
			return fmt.Errorf("redfish: chassis: %w", err)
		}
		found = true
		h.Health = max(h.Health, parseHealth(c.Status.Health))
		// The power supplies first: on the subsystem path every fan costs
		// a request of its own, and a long fan list must not use up the
		// budget before a critical power supply is read.
		if err := q.power(ctx, h, &c); err != nil {
			return err
		}
		if err := q.thermal(ctx, h, &c); err != nil {
			return err
		}
	}
	if !found {
		return ErrNoSystem
	}
	return nil
}

// systemPath returns the path of the system to read ("" without one).
func (q *poller) systemPath(ctx context.Context, root *serviceRoot, id string) (string, error) {
	if root.Systems.ID == "" {
		return "", nil
	}
	if id != "" {
		return strings.TrimSuffix(root.Systems.ID, "/") + "/" + id, nil
	}
	var systems systemCollection
	if err := q.get(ctx, root.Systems.ID, &systems); err != nil {
		return "", fmt.Errorf("redfish: systems: %w", err)
	}
	if len(systems.Members) == 0 {
		return "", nil
	}
	return systems.Members[0].ID, nil
}

// chassisPaths returns the paths of the chassis to read.
func (q *poller) chassisPaths(ctx context.Context, root *serviceRoot, ids []string) ([]string, error) {
	if root.Chassis.ID == "" {
		return nil, nil
	}
	if len(ids) > 0 {
		out := make([]string, len(ids))
		for i, id := range ids {
			out[i] = strings.TrimSuffix(root.Chassis.ID, "/") + "/" + id
		}
		return out, nil
	}
	var members systemCollection
	if err := q.get(ctx, root.Chassis.ID, &members); err != nil {
		return nil, fmt.Errorf("redfish: chassis collection: %w", err)
	}
	out := make([]string, 0, len(members.Members))
	for _, m := range members.Members {
		out = append(out, m.ID)
	}
	return out, nil
}

// thermal reads the temperatures and fans of a chassis: the legacy
// Thermal resource if the chassis links it — one request, and only it
// carries the thresholds of the device (UpperThresholdCritical) —
// otherwise ThermalSubsystem.
func (q *poller) thermal(ctx context.Context, h *Health, c *chassis) error {
	switch {
	case c.Thermal.ID != "":
		var t legacyThermal
		if err := q.get(ctx, c.Thermal.ID, &t); err != nil {
			return fmt.Errorf("redfish: thermal: %w", err)
		}
		for _, s := range t.Temperatures {
			if s.ReadingCelsius == nil || absent(s.Status) {
				continue
			}
			h.Temperatures = append(h.Temperatures, Temperature{
				Name: c.ID + "/" + s.Name, Celsius: *s.ReadingCelsius,
				UpperNonCritical: s.UpperThresholdNonCritical, UpperCritical: s.UpperThresholdCritical,
				Health: parseHealth(s.Status.Health),
			})
		}
		for _, f := range t.Fans {
			if absent(f.Status) {
				continue
			}
			fan := Fan{Name: c.ID + "/" + firstNonEmpty(f.Name, f.FanName), Health: parseHealth(f.Status.Health)}
			if f.Reading != nil && (f.ReadingUnits == "" || strings.EqualFold(f.ReadingUnits, "RPM")) {
				fan.RPM = f.Reading
			}
			h.Fans = append(h.Fans, fan)
		}
	case c.ThermalSubsystem.ID != "":
		var ts thermalSubsystem
		if err := q.get(ctx, c.ThermalSubsystem.ID, &ts); err != nil {
			return fmt.Errorf("redfish: thermal subsystem: %w", err)
		}
		if ts.ThermalMetrics.ID != "" {
			var tm thermalMetrics
			if err := q.get(ctx, ts.ThermalMetrics.ID, &tm); err != nil {
				return fmt.Errorf("redfish: thermal metrics: %w", err)
			}
			for _, r := range tm.TemperatureReadingsCelsius {
				if r.Reading == nil {
					continue
				}
				name := r.DeviceName
				if name == "" {
					name = lastSegment(r.DataSourceURI)
				}
				h.Temperatures = append(h.Temperatures, Temperature{Name: c.ID + "/" + name, Celsius: *r.Reading})
			}
		}
		if ts.Fans.ID != "" {
			var fans systemCollection
			if err := q.get(ctx, ts.Fans.ID, &fans); err != nil {
				return fmt.Errorf("redfish: fans: %w", err)
			}
			for _, m := range fans.Members {
				var f fanResource
				if err := q.get(ctx, m.ID, &f); err != nil {
					return fmt.Errorf("redfish: fan: %w", err)
				}
				h.Fans = append(h.Fans, Fan{Name: c.ID + "/" + firstNonEmpty(f.ID, f.Name, lastSegment(m.ID)),
					RPM: f.SpeedPercent.SpeedRPM, Health: parseHealth(f.Status.Health)})
			}
		}
	}
	return nil
}

// power reads the power supplies of a chassis: the legacy Power resource
// if the chassis links it (one request instead of two per supply),
// otherwise PowerSubsystem.
func (q *poller) power(ctx context.Context, h *Health, c *chassis) error {
	switch {
	case c.Power.ID != "":
		var p legacyPower
		if err := q.get(ctx, c.Power.ID, &p); err != nil {
			return fmt.Errorf("redfish: power: %w", err)
		}
		for i, s := range p.PowerSupplies {
			if absent(s.Status) {
				continue
			}
			name := firstNonEmpty(s.Name, s.MemberID, strconv.Itoa(i))
			h.PowerSupplies = append(h.PowerSupplies, PowerSupply{Name: c.ID + "/" + name,
				InputWatts: s.PowerInputWatts, Health: parseHealth(s.Status.Health)})
		}
	case c.PowerSubsystem.ID != "":
		var ps powerSubsystem
		if err := q.get(ctx, c.PowerSubsystem.ID, &ps); err != nil {
			return fmt.Errorf("redfish: power subsystem: %w", err)
		}
		if ps.PowerSupplies.ID == "" {
			return nil
		}
		var members systemCollection
		if err := q.get(ctx, ps.PowerSupplies.ID, &members); err != nil {
			return fmt.Errorf("redfish: power supplies: %w", err)
		}
		for _, m := range members.Members {
			var psu powerSupplyResource
			if err := q.get(ctx, m.ID, &psu); err != nil {
				return fmt.Errorf("redfish: power supply: %w", err)
			}
			if absent(psu.Status) {
				continue
			}
			out := PowerSupply{Name: c.ID + "/" + firstNonEmpty(psu.ID, psu.Name, lastSegment(m.ID)), Health: parseHealth(psu.Status.Health)}
			if psu.Metrics.ID != "" {
				var pm powerSupplyMetrics
				err := q.get(ctx, psu.Metrics.ID, &pm)
				if err != nil && !errors.Is(err, errRequestLimit) {
					return fmt.Errorf("redfish: power supply metrics: %w", err)
				}
				out.InputWatts = pm.InputPowerWatts.Reading
				if err != nil {
					h.PowerSupplies = append(h.PowerSupplies, out)
					return err
				}
			}
			h.PowerSupplies = append(h.PowerSupplies, out)
		}
	}
	return nil
}

// absent reports a sensor or unit the device lists but has not installed.
func absent(s status) bool {
	return strings.EqualFold(s.State, "Absent")
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func lastSegment(path string) string {
	path = strings.TrimSuffix(path, "/")
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}
