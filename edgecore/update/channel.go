package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Release channels (COL-06): stable receives only stable releases, beta
// receives beta releases and every stable release.
const (
	ChannelStable = "stable"
	ChannelBeta   = "beta"
)

// ErrChannelMismatch refuses a release of a channel the updater is not
// subscribed to.
var ErrChannelMismatch = errors.New("update: release is not on the subscribed channel")

// ValidChannel reports whether ch is a known release channel.
func ValidChannel(ch string) bool { return ch == ChannelStable || ch == ChannelBeta }

// ChannelAccepts reports whether a release of channel release may be
// installed by an updater subscribed to subscribed. A release without a
// channel is a stable release.
func ChannelAccepts(subscribed, release string) bool {
	if release == "" {
		release = ChannelStable
	}
	return release == subscribed || (subscribed == ChannelBeta && release == ChannelStable)
}

// HTTPSource reads the release manifest of the release workflow
// (<artifact>-<os>-<arch>.release.json) from <BaseURL>/<Channel>/.
type HTTPSource struct {
	BaseURL string
	Channel string
	// Artifact is the binary name, e.g. "reticora-collector".
	Artifact string
	Client   *http.Client
}

// ManifestURL is the URL of the manifest for this platform.
func (s *HTTPSource) ManifestURL() string {
	return fmt.Sprintf("%s/%s/%s-%s-%s.release.json", strings.TrimRight(s.BaseURL, "/"), s.Channel, s.Artifact, runtime.GOOS, runtime.GOARCH)
}

// Latest implements Source.
func (s *HTTPSource) Latest(ctx context.Context) (*ReleaseInfo, error) {
	if !ValidChannel(s.Channel) {
		return nil, fmt.Errorf("update: unknown channel %q (stable or beta)", s.Channel)
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.ManifestURL(), http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release manifest: status %d", resp.StatusCode)
	}
	var rel ReleaseInfo
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("release manifest: %w", err)
	}
	return &rel, nil
}

// CompareVersions compares two semantic versions ("v1.2.3", "1.2.3-beta.1")
// and returns -1, 0 or 1; ok is false when one of them does not parse.
func CompareVersions(a, b string) (cmp int, ok bool) {
	va, oka := parseVersion(a)
	vb, okb := parseVersion(b)
	if !oka || !okb {
		return 0, false
	}
	for i := range 3 {
		if va.core[i] != vb.core[i] {
			if va.core[i] < vb.core[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return comparePre(va.pre, vb.pre), true
}

type version struct {
	core [3]int
	pre  []string
}

func parseVersion(s string) (version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v version
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v.core[i] = n
	}
	if hasPre {
		if pre == "" {
			return v, false
		}
		v.pre = strings.Split(pre, ".")
	}
	return v, true
}

// comparePre orders pre-release identifiers per SemVer 2.0: a version
// without pre-release is newer than one with.
func comparePre(a, b []string) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return 1
	case len(b) == 0:
		return -1
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := compareIdent(a[i], b[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

func compareIdent(a, b string) int {
	na, ea := strconv.Atoi(a)
	nb, eb := strconv.Atoi(b)
	switch {
	case ea == nil && eb == nil:
		return cmpInt(na, nb)
	case ea == nil:
		return -1
	case eb == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
