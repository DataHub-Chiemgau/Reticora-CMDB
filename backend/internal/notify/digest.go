package notify

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Digests of NTF-04 (WP-508, F-156): the e-mails of a non-critical
// category a user bundles (Preference.Digest) are not delivered one by
// one. The notifier stores them as digest items; the scheduler kind
// notify.digest turns the items of an ended window into one delivery per
// channel, recipient, category and window, which then goes through the
// delivery queue like every other (retry, quiet hours, muted categories,
// entitlement). Critical notifications, the in-app center and webhooks are
// never bundled.

// DigestKind is the scheduler kind of the digest run.
const DigestKind = "notify.digest"

// Digest windows.
const (
	DigestHourly = "hourly"
	DigestDaily  = "daily"
)

// DefaultDigest is the digest of a user without a stored preference:
// review notifications by e-mail hourly.
func DefaultDigest() map[string]string { return map[string]string{"review": DigestHourly} }

// DigestCategories are the categories a digest may bundle: those of the
// subscriptions with a non-critical event. A category whose events are all
// critical (collector, invitation, certificate, apikey) is never bundled.
func DigestCategories() []string {
	var out []string
	for _, sub := range Subscriptions {
		if !sub.Critical && !slices.Contains(out, sub.Category) {
			out = append(out, sub.Category)
		}
	}
	sort.Strings(out)
	return out
}

// validateDigest checks the categories and windows of a digest.
func validateDigest(digest map[string]string) error {
	allowed := DigestCategories()
	for category, window := range digest {
		if !slices.Contains(allowed, category) {
			return fmt.Errorf("%w: digest: unknown or critical category %q (allowed: %s)", ErrValidation, category,
				strings.Join(allowed, ", "))
		}
		if window != DigestHourly && window != DigestDaily {
			return fmt.Errorf("%w: digest: unknown window %q for %s (hourly or daily)", ErrValidation, window, category)
		}
	}
	return nil
}

// digestWindow returns the window a preference bundles category in, empty
// when it is not bundled; without a preference the default applies.
func digestWindow(p *Preference, category string) string {
	if p == nil {
		return DefaultDigest()[category]
	}
	return p.Digest[category]
}

// windowEnd returns the end of the window that contains t: the next full
// hour, or the next midnight in the recipient's time zone.
func windowEnd(window, timeZone string, t time.Time) time.Time {
	if window == DigestDaily {
		loc, err := time.LoadLocation(timeZone)
		if err != nil || timeZone == "" {
			loc = time.UTC
		}
		local := t.In(loc)
		return time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, loc).UTC()
	}
	return t.UTC().Truncate(time.Hour).Add(time.Hour)
}

// DigestItem is one bundled notification (notification_digest_item).
type DigestItem struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	ChannelID      string         `json:"channel_id"`
	Recipient      Recipient      `json:"recipient"`
	RecipientKey   string         `json:"-"`
	Category       string         `json:"category"`
	Window         string         `json:"window"`
	WindowEnd      time.Time      `json:"window_end"`
	Subject        string         `json:"subject"`
	Body           string         `json:"body"`
	Data           map[string]any `json:"data,omitempty"`
	DedupKey       string         `json:"dedup_key,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

// DigestStore keeps the digest items.
type DigestStore interface {
	// AddItems stores new items; one with a taken dedup key is skipped.
	AddItems(ctx context.Context, orgID string, items []DigestItem) (created int, err error)
	// Bundle hands the items of the windows ended at now, grouped by
	// channel, recipient, category and window, to build and queues the
	// delivery it returns for each group, once: the items are marked with
	// it in the same transaction, the delivery's dedup key keeps a group
	// from being queued twice. It returns how many deliveries it queued.
	Bundle(ctx context.Context, now time.Time, build func(items []DigestItem) QueuedDelivery) (int, error)
}

// WithDigest makes the notifier bundle the e-mails of the categories in
// the recipients' digest (NTF-04). Without a store every notification is
// delivered on its own.
func (n *Notifier) WithDigest(store DigestStore) *Notifier {
	n.digest = store
	return n
}

// RunDigest queues the digests of the ended windows and returns how many
// it queued (scheduler kind notify.digest).
func (n *Notifier) RunDigest(ctx context.Context) (int, error) {
	if n.digest == nil {
		return 0, nil
	}
	return n.digest.Bundle(ctx, n.now(), n.buildDigest)
}

// bundled reports, for an e-mail row of req, the window its recipient
// bundles the category in (empty: deliver now). prefs caches the
// preferences of the request's recipients.
func (n *Notifier) bundled(ctx context.Context, orgID string, req *Request, kind string, r Recipient,
	prefs map[string]*Preference) (window, timeZone string, err error) {
	if n.digest == nil || req.Critical || kind != KindEmail || r.UserID == "" || r.Email == "" {
		return "", "", nil
	}
	pref, ok := prefs[r.UserID]
	if !ok && n.prefs != nil {
		if pref, err = n.prefs.Preference(ctx, orgID, r.UserID); err != nil {
			return "", "", err
		}
		prefs[r.UserID] = pref
	}
	window = digestWindow(pref, req.Category)
	if pref != nil {
		timeZone = pref.TimeZone
	}
	return window, timeZone, nil
}

// buildDigest renders the delivery of one group of digest items: a list of
// the subjects (German, then English), the entries with their texts and
// data in the delivery's data.
func (n *Notifier) buildDigest(items []DigestItem) QueuedDelivery {
	first := items[0]
	sort.SliceStable(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	lines := make([]string, len(items))
	entries := make([]any, len(items))
	for i := range items {
		it := &items[i]
		lines[i] = "- " + it.CreatedAt.UTC().Format("2006-01-02 15:04 UTC") + ": " + it.Subject
		entries[i] = map[string]any{"subject": it.Subject, "body": it.Body, "data": it.Data,
			"created_at": it.CreatedAt.UTC().Format(time.RFC3339)}
	}
	list := strings.Join(lines, "\n")
	period := map[string][2]string{DigestHourly: {"der letzten Stunde", "of the last hour"},
		DigestDaily: {"des letzten Tages", "of the last day"}}[first.Window]
	end := first.WindowEnd.UTC().Format(time.RFC3339)
	return QueuedDelivery{OrganizationID: first.OrganizationID, ChannelID: first.ChannelID, Recipient: first.Recipient,
		RecipientKey: first.RecipientKey, Category: first.Category,
		Subject: fmt.Sprintf("Zusammenfassung %s: %d Benachrichtigungen", first.Category, len(items)),
		Body: text(fmt.Sprintf("%d Benachrichtigungen (%s) %s:\n%s", len(items), first.Category, period[0], list),
			fmt.Sprintf("%d notifications (%s) %s:\n%s", len(items), first.Category, period[1], list)),
		Data: map[string]any{"digest": true, "category": first.Category, "window": first.Window, "window_end": end,
			"count": len(items), "entries": entries},
		DedupKey: "digest:" + first.Category + ":" + first.Window + ":" + end, Status: StatusPending,
		MaxAttempts: n.MaxAttempts, NextAttemptAt: n.now()}
}
