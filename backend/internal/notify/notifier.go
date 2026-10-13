package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
)

// Request is one notification of the application: what happened, to whom.
type Request struct {
	Category string
	// Critical notifications ignore quiet hours and muted categories.
	Critical bool
	Subject  string
	Body     string
	Data     map[string]any
	// DedupKey identifies the fact: the same key reaches a recipient over
	// a channel once.
	DedupKey   string
	Recipients []Recipient
	// ChannelIDs limits the channels; empty: every enabled channel.
	ChannelIDs []string
}

// Delivery statuses (NTF-04).
const (
	StatusPending    = "pending"
	StatusSending    = "sending"
	StatusSent       = "sent"
	StatusFailed     = "failed"
	StatusSuppressed = "suppressed"
)

// QueuedDelivery is one row of notification_delivery.
type QueuedDelivery struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	ChannelID      string         `json:"channel_id"`
	Recipient      Recipient      `json:"recipient"`
	RecipientKey   string         `json:"-"`
	Recipients     []Recipient    `json:"recipients,omitempty"`
	Category       string         `json:"category"`
	Critical       bool           `json:"critical"`
	Subject        string         `json:"subject"`
	Body           string         `json:"body"`
	Data           map[string]any `json:"data,omitempty"`
	DedupKey       string         `json:"dedup_key,omitempty"`
	Status         string         `json:"status"`
	Attempts       int            `json:"attempts"`
	MaxAttempts    int            `json:"max_attempts"`
	NextAttemptAt  time.Time      `json:"next_attempt_at"`
	LastError      string         `json:"last_error,omitempty"`
	ProviderID     string         `json:"provider_id,omitempty"`
	SentAt         *time.Time     `json:"sent_at,omitempty"`
}

// Preference is a user's quiet hours and muted categories.
type Preference struct {
	QuietStart      string   `json:"quiet_start,omitempty"` // HH:MM
	QuietEnd        string   `json:"quiet_end,omitempty"`
	TimeZone        string   `json:"time_zone"`
	MutedCategories []string `json:"muted_categories"`
}

// Validate checks the preference.
func (p *Preference) Validate() error {
	if p.TimeZone == "" {
		p.TimeZone = "Europe/Berlin"
	}
	if _, err := time.LoadLocation(p.TimeZone); err != nil {
		return fmt.Errorf("%w: unknown time zone %q", ErrValidation, p.TimeZone)
	}
	if (p.QuietStart == "") != (p.QuietEnd == "") {
		return fmt.Errorf("%w: quiet hours need a start and an end", ErrValidation)
	}
	for _, v := range []string{p.QuietStart, p.QuietEnd} {
		if v == "" {
			continue
		}
		if _, err := time.Parse("15:04", v); err != nil {
			return fmt.Errorf("%w: quiet hours are HH:MM, got %q", ErrValidation, v)
		}
	}
	if p.MutedCategories == nil {
		p.MutedCategories = []string{}
	}
	return nil
}

// QuietUntil returns the end of the quiet hours that contain t, or the
// zero time when t is not quiet. Quiet hours may span midnight.
func (p *Preference) QuietUntil(t time.Time) time.Time {
	if p.QuietStart == "" {
		return time.Time{}
	}
	loc, err := time.LoadLocation(p.TimeZone)
	if err != nil {
		return time.Time{}
	}
	local := t.In(loc)
	start, _ := time.Parse("15:04", p.QuietStart)
	end, _ := time.Parse("15:04", p.QuietEnd)
	at := func(day time.Time, hm time.Time) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), hm.Hour(), hm.Minute(), 0, 0, loc)
	}
	todayStart, todayEnd := at(local, start), at(local, end)
	if !todayEnd.After(todayStart) { // spans midnight
		if !local.Before(todayStart) {
			return todayEnd.AddDate(0, 0, 1).UTC()
		}
		if local.Before(todayEnd) {
			return todayEnd.UTC()
		}
		return time.Time{}
	}
	if !local.Before(todayStart) && local.Before(todayEnd) {
		return todayEnd.UTC()
	}
	return time.Time{}
}

// DeliveryStore persists the deliveries.
type DeliveryStore interface {
	// Enqueue stores new deliveries; one with a taken dedup key is skipped.
	Enqueue(ctx context.Context, orgID string, rows []QueuedDelivery) (created int, err error)
	// ClaimDue marks due deliveries of all organizations as sending; a
	// delivery left sending longer than the lease is due again.
	ClaimDue(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]QueuedDelivery, error)
	// Finish stores the outcome of an attempt (status, next attempt,
	// error, provider id, sent time).
	Finish(ctx context.Context, d *QueuedDelivery) error
	// List returns the deliveries of an organization, newest first.
	List(ctx context.Context, orgID string, limit int) ([]QueuedDelivery, error)
}

// PreferenceStore reads and writes user preferences.
type PreferenceStore interface {
	Preference(ctx context.Context, orgID, userID string) (*Preference, error)
	SetPreference(ctx context.Context, orgID, userID string, p *Preference) error
}

// Notifier turns requests into deliveries and works them off (NTF-04).
type Notifier struct {
	channels    Repository
	store       DeliveryStore
	prefs       PreferenceStore
	sender      *Sender
	MaxAttempts int
	// Backoff is the delay after the n-th failed attempt.
	Backoff func(n int) time.Duration
	Batch   int
	Lease   time.Duration
	now     func() time.Time
}

// NewNotifier creates a notifier.
func NewNotifier(channels Repository, store DeliveryStore, prefs PreferenceStore, sender *Sender) *Notifier {
	return &Notifier{channels: channels, store: store, prefs: prefs, sender: sender, MaxAttempts: 5,
		Backoff: func(n int) time.Duration { return time.Minute * time.Duration(1<<min(n, 10)) },
		Batch:   50, Lease: 10 * time.Minute, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the clock (tests).
func (n *Notifier) WithClock(now func() time.Time) *Notifier {
	n.now = now
	return n
}

// Notify queues the request on the organization's channels: per recipient
// for e-mail (with address) and in-app (users), once for a webhook. It
// returns how many deliveries it queued (dedup skips the known ones).
func (n *Notifier) Notify(ctx context.Context, orgID string, req *Request) (int, error) {
	if strings.TrimSpace(req.Category) == "" || strings.TrimSpace(req.Subject) == "" {
		return 0, fmt.Errorf("%w: a notification needs a category and a subject", ErrValidation)
	}
	channels, err := n.channels.List(ctx, orgID)
	if err != nil {
		return 0, err
	}
	now := n.now()
	var rows []QueuedDelivery
	for i := range channels {
		ch := &channels[i]
		if !ch.Enabled || (len(req.ChannelIDs) > 0 && !slices.Contains(req.ChannelIDs, ch.ID)) {
			continue
		}
		base := QueuedDelivery{OrganizationID: orgID, ChannelID: ch.ID, Category: req.Category, Critical: req.Critical,
			Subject: req.Subject, Body: req.Body, Data: req.Data, DedupKey: req.DedupKey, Status: StatusPending,
			MaxAttempts: n.MaxAttempts, NextAttemptAt: now}
		switch ch.Kind {
		case KindWebhook:
			row := base
			row.RecipientKey, row.Recipients = "*", req.Recipients
			rows = append(rows, row)
		default:
			for _, r := range req.Recipients {
				key := ""
				switch {
				case ch.Kind == KindEmail && r.Email != "":
					key = "mail:" + strings.ToLower(r.Email)
				case ch.Kind == KindInApp && r.UserID != "":
					key = "user:" + r.UserID
				}
				if key == "" {
					continue
				}
				row := base
				row.Recipient, row.RecipientKey = r, key
				rows = append(rows, row)
			}
		}
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return n.store.Enqueue(ctx, orgID, rows)
}

// ProcessDue works off the due deliveries and returns how many it sent.
func (n *Notifier) ProcessDue(ctx context.Context) (int, error) {
	due, err := n.store.ClaimDue(ctx, n.now(), n.Batch, n.Lease)
	if err != nil {
		return 0, err
	}
	sent := 0
	var firstErr error
	for i := range due {
		ok, err := n.attempt(ctx, &due[i])
		if ok {
			sent++
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return sent, firstErr
}

func (n *Notifier) attempt(ctx context.Context, d *QueuedDelivery) (bool, error) {
	now := n.now()
	ch, err := n.channels.Get(ctx, d.OrganizationID, d.ChannelID)
	if errors.Is(err, ErrNotFound) || (err == nil && !ch.Enabled) {
		d.Status, d.LastError = StatusSuppressed, "channel removed or disabled"
		return false, n.store.Finish(ctx, d)
	}
	if err != nil {
		return false, n.retry(ctx, d, err)
	}
	if d.Recipient.UserID != "" && !d.Critical && n.prefs != nil {
		pref, perr := n.prefs.Preference(ctx, d.OrganizationID, d.Recipient.UserID)
		if perr != nil {
			return false, n.retry(ctx, d, perr)
		}
		if pref != nil {
			if slices.Contains(pref.MutedCategories, d.Category) {
				d.Status, d.LastError = StatusSuppressed, "category muted by the recipient"
				return false, n.store.Finish(ctx, d)
			}
			if until := pref.QuietUntil(now); !until.IsZero() {
				// Not an attempt: the delivery waits for the end of the
				// quiet hours.
				d.Attempts--
				d.Status, d.NextAttemptAt = StatusPending, until
				return false, n.store.Finish(ctx, d)
			}
		}
	}
	to := []Recipient{d.Recipient}
	if d.RecipientKey == "*" {
		to = d.Recipients
	}
	delivery, err := n.sender.Send(ctx, ch, &Message{Category: d.Category, Subject: d.Subject, Body: d.Body, To: to, Data: d.Data})
	if err != nil {
		return false, n.retry(ctx, d, err)
	}
	d.Status, d.ProviderID, d.LastError = StatusSent, delivery.ProviderID, ""
	sentAt := now
	d.SentAt = &sentAt
	return true, n.store.Finish(ctx, d)
}

// retry schedules the next attempt or gives up after MaxAttempts.
func (n *Notifier) retry(ctx context.Context, d *QueuedDelivery, cause error) error {
	d.LastError = cause.Error()
	if d.Attempts >= d.MaxAttempts {
		d.Status = StatusFailed
		slog.Warn("notification delivery failed", "delivery", d.ID, "channel", d.ChannelID, "error", cause)
	} else {
		d.Status, d.NextAttemptAt = StatusPending, n.now().Add(n.Backoff(d.Attempts))
	}
	if err := n.store.Finish(ctx, d); err != nil {
		return err
	}
	return cause
}

// Run works off due deliveries every interval until ctx ends (worker
// notifier, OPS-02).
func (n *Notifier) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		_, err := n.ProcessDue(ctx)
		failures := 0
		if err != nil {
			failures = 1
		}
		observability.ObserveWorkerRun("notifier", failures)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// MemoryStore keeps deliveries and preferences in memory.
type MemoryStore struct {
	mu    sync.Mutex
	rows  []*QueuedDelivery
	prefs map[string]Preference
	seq   int
}

// NewMemoryStore creates an empty store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{prefs: map[string]Preference{}} }

// Enqueue implements DeliveryStore.
func (s *MemoryStore) Enqueue(_ context.Context, orgID string, rows []QueuedDelivery) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	created := 0
	for i := range rows {
		r := rows[i]
		if r.DedupKey != "" && slices.ContainsFunc(s.rows, func(x *QueuedDelivery) bool {
			return x.OrganizationID == orgID && x.ChannelID == r.ChannelID && x.RecipientKey == r.RecipientKey && x.DedupKey == r.DedupKey
		}) {
			continue
		}
		s.seq++
		r.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", s.seq)
		s.rows = append(s.rows, &r)
		created++
	}
	return created, nil
}

// ClaimDue implements DeliveryStore.
func (s *MemoryStore) ClaimDue(_ context.Context, now time.Time, limit int, _ time.Duration) ([]QueuedDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []QueuedDelivery
	for _, r := range s.rows {
		if r.Status != StatusPending || r.NextAttemptAt.After(now) {
			continue
		}
		r.Status = StatusSending
		r.Attempts++
		out = append(out, *r)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Finish implements DeliveryStore.
func (s *MemoryStore) Finish(_ context.Context, d *QueuedDelivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rows {
		if r.ID == d.ID {
			*r = *d
			return nil
		}
	}
	return ErrNotFound
}

// List implements DeliveryStore.
func (s *MemoryStore) List(_ context.Context, orgID string, limit int) ([]QueuedDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []QueuedDelivery
	for i := len(s.rows) - 1; i >= 0 && len(out) < limit; i-- {
		if s.rows[i].OrganizationID == orgID {
			out = append(out, *s.rows[i])
		}
	}
	return out, nil
}

// Preference implements PreferenceStore.
func (s *MemoryStore) Preference(_ context.Context, orgID, userID string) (*Preference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.prefs[orgID+"/"+userID]
	if !ok {
		return nil, nil
	}
	return &p, nil
}

// SetPreference implements PreferenceStore.
func (s *MemoryStore) SetPreference(_ context.Context, orgID, userID string, p *Preference) error {
	if err := p.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *p
	cp.MutedCategories = append([]string(nil), p.MutedCategories...)
	sort.Strings(cp.MutedCategories)
	s.prefs[orgID+"/"+userID] = cp
	return nil
}
