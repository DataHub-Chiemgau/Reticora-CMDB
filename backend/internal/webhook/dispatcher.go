package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
)

// retrySchedule is the backoff of WHK-01: the delays before the second to
// the sixth attempt; after the sixth failed attempt the delivery is dead.
var retrySchedule = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, time.Hour, 6 * time.Hour}

// Deactivation of WHK-01: a subscription with deadLimit dead deliveries
// within deadWindow is deactivated and its owners are notified.
const (
	deadLimit  = 3
	deadWindow = 24 * time.Hour
)

const (
	defaultMaxAttempts  = 6
	defaultBaseBackoff  = 30 * time.Second
	defaultMaxBackoff   = time.Hour
	defaultPollInterval = 15 * time.Second
	defaultClaimBatch   = 25
	defaultClaimLease   = 2 * time.Minute
)

// Delivery captures one webhook delivery attempt as returned by the API.
type Delivery struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	SubscriptionID string    `json:"subscription_id"`
	Event          string    `json:"event"`
	URL            string    `json:"url"`
	Attempt        int       `json:"attempt"`
	StatusCode     int       `json:"status_code,omitempty"`
	Success        bool      `json:"success"`
	Error          string    `json:"error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type deliveryJob struct {
	deliveryID string
	orgID      string
	event      string
	payload    []byte
	attempt    int
	maxAttempt int
	sub        Subscription
}

// DispatcherOptions configures durable delivery behaviour.
type DispatcherOptions struct {
	// Deliveries persists queued deliveries and their retry schedule. When nil
	// the dispatcher degrades to an in-process retry sequence.
	Deliveries DeliveryStore
	// MaxAttempts is the total number of attempts per delivery.
	MaxAttempts int
	// BaseBackoff is the delay before the second attempt; it doubles per
	// attempt up to MaxBackoff.
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	// schedule replaces BaseBackoff/MaxBackoff with fixed delays per attempt
	// (WHK-01); set when neither is given.
	schedule []time.Duration
	// PollInterval controls how often the retry worker looks for due
	// deliveries. A negative value disables the worker.
	PollInterval time.Duration
	// Events reports a delivery moved to the dead-letter queue as
	// webhook.dead (NTF-05); optional.
	Events events.Emitter
	// Access decides for a subscription bound to a service account whether
	// the account may read the event's object (RBA-08). Without it, bound
	// subscriptions receive nothing (fail-closed).
	Access SubscriberAccess
	// Objects reads the client and site of the objects an event references
	// by ID when its payload carries no client_id/site_id (scope.go).
	// Without it such events reach only unbound subscriptions and accounts
	// that read org-wide.
	Objects ObjectResolver
}

// SubscriberAccess checks the read right of a service account on the
// objects of an event with the given permission. objects holds the client
// and site of every object ("" is NULL); nil means the scope of the event is
// unknown, which only an account reading org-wide may read.
type SubscriberAccess interface {
	CanReadEvent(ctx context.Context, orgID, serviceAccountID, permission string, objects []ObjectScope) (bool, error)
}

// EventReadPermission is the permission needed to read the object of an
// event; "" for events without a known object (bound subscriptions do not
// receive them).
func EventReadPermission(event string) string {
	prefix, _, _ := strings.Cut(event, ".")
	switch prefix {
	case "ci", "relationship", "ci_type", "ci_attribute", "lifecycle", "override":
		return "ci:read"
	case "asset", "inventory", "reservation", "composition":
		return "asset:read"
	case "discovery", "reconciliation":
		return "discovery:read"
	}
	return ""
}

// covers reports whether a subscription with the scope snapshot s receives
// an event about objects (eventObjects; nil: scope unknown), E-98: an
// org-wide snapshot (none, or no dimension restricted) receives every event;
// a restricted one only events whose objects all lie in it, with a client
// in its clients and a site in its sites where it restricts them. Events of
// unknown scope and events about objects of the whole organization
// (organizational metadata, rows without client and site) reach org-wide
// snapshots only. The objects carry no team: the team dimension does not
// restrict them, like row level security of ci, asset and quantity_item.
func (s *Scope) covers(objects []ObjectScope) bool {
	if s.orgWide() {
		return true
	}
	if len(objects) == 0 {
		return false
	}
	for _, o := range objects {
		if o.ClientID == "" && o.SiteID == "" {
			return false
		}
		if (s.Clients != nil && !containsID(s.Clients, o.ClientID)) || (s.Sites != nil && !containsID(s.Sites, o.SiteID)) {
			return false
		}
	}
	return true
}

// mayDeliver reports whether a subscription receives the event: the objects
// of the event must lie in the scope snapshot of the subscription (covers,
// E-98); then unbound subscriptions receive the event, bound ones only when
// their service account may read its objects as well, so a binding never
// widens the scope of the subscription's creator. objects is the resolved
// scope of the event (eventObjects; nil: unknown).
func (d *Dispatcher) mayDeliver(ctx context.Context, orgID, event string, objects []ObjectScope, sub *Subscription) bool {
	if !sub.Scope.covers(objects) {
		return false
	}
	if sub.ServiceAccountID == "" {
		return true
	}
	permission := EventReadPermission(event)
	if permission == "" || d.opts.Access == nil {
		return false
	}
	ok, err := d.opts.Access.CanReadEvent(ctx, orgID, sub.ServiceAccountID, permission, objects)
	if err != nil {
		slog.Error("check webhook subscriber rights failed", "error", err, "organization_id", orgID, "subscription_id", sub.ID)
		return false
	}
	return ok
}

// Dispatcher performs background webhook deliveries. Every dispatched event is
// persisted through the DeliveryStore before the first HTTP attempt, so pending
// retries survive a restart and can be picked up by any replica.
type Dispatcher struct {
	repo       Repository
	client     *http.Client
	store      DeliveryStore
	opts       DispatcherOptions
	queue      chan deliveryJob
	wg         sync.WaitGroup
	stop       chan struct{}
	stopOnce   sync.Once
	seq        uint64
	mu         sync.RWMutex
	deliveries []Delivery
	now        func() time.Time
}

// NewDispatcher creates a dispatcher with a background worker.
func NewDispatcher(repo Repository, client *http.Client, opts ...DispatcherOptions) *Dispatcher {
	if client == nil {
		// Subscriber URLs are user input: deliveries go through the egress
		// client, which blocks internal destinations (SEC-08).
		client = egress.NewClient(egress.Options{})
	}

	options := DispatcherOptions{}
	if len(opts) > 0 {
		options = opts[0]
	}
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = defaultMaxAttempts
	}
	// Without an explicit backoff the schedule of WHK-01 applies.
	if options.BaseBackoff <= 0 && options.MaxBackoff <= 0 {
		options.schedule = retrySchedule
	}
	if options.BaseBackoff <= 0 {
		options.BaseBackoff = defaultBaseBackoff
	}
	if options.MaxBackoff <= 0 {
		options.MaxBackoff = defaultMaxBackoff
	}
	if options.PollInterval == 0 {
		options.PollInterval = defaultPollInterval
	}

	d := &Dispatcher{
		repo:   repo,
		client: client,
		store:  options.Deliveries,
		opts:   options,
		queue:  make(chan deliveryJob, 128),
		stop:   make(chan struct{}),
		now:    func() time.Time { return time.Now().UTC() },
	}

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		for job := range d.queue {
			d.attempt(job)
		}
	}()

	if d.store != nil && options.PollInterval > 0 {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.retryLoop(options.PollInterval)
		}()
	}

	return d
}

// orgScoped attaches the org-wide tenant scope of orgID, under which the
// dispatcher reads subscriptions outside of or beyond the acting request.
func orgScoped(ctx context.Context, orgID string) context.Context {
	scope := database.OrgWideScope(orgID, "")
	return database.ContextWithTenantScope(ctx, &scope)
}

// Dispatch persists and queues webhook deliveries for matching subscriptions.
func (d *Dispatcher) Dispatch(ctx context.Context, orgID, event string, payload any) {
	d.dispatchAs(ctx, orgID, event, nil, payload)
}

// legacyNames are the former names of the events of EVT-02 that existing
// subscriptions still use: they receive the event under its new name.
var legacyNames = map[string][]string{
	events.CIChanged:           {"ci.updated"},
	events.RelationshipRemoved: {"relationship.deleted"},
}

// Consume implements events.Consumer: the outbox publisher hands every
// committed domain event to the subscriptions of its type and of its
// former names (WP-136). A status-only change also reaches the
// subscriptions of ci.status_changed.
func (d *Dispatcher) Consume(ctx context.Context, env *events.Envelope) error {
	aliases := append([]string(nil), legacyNames[env.Type]...)
	if env.Type == events.CIChanged {
		var data struct {
			ChangedFields []string `json:"changed_fields"`
		}
		if json.Unmarshal(env.Data, &data) == nil && len(data.ChangedFields) == 1 && data.ChangedFields[0] == "status" {
			aliases = append(aliases, "ci.status_changed")
		}
	}
	d.dispatchAs(ctx, env.OrgID, env.Type, aliases, env.Data)
	return nil
}

func (d *Dispatcher) dispatchAs(ctx context.Context, orgID, event string, aliases []string, payload any) {
	if d == nil || d.repo == nil {
		return
	}

	// The lookup finds every subscription of the organization, independent
	// of the acting principal's client scope: it runs org-wide on behalf of
	// the organization (E-08; WP-022 moves the dispatcher to a system
	// principal per organization). mayDeliver then applies the scope
	// snapshot of each subscription (E-98).
	var subs []Subscription
	seen := map[string]bool{}
	for _, name := range append([]string{event}, aliases...) {
		named, err := d.repo.ListByEvent(orgScoped(ctx, orgID), orgID, name)
		if err != nil {
			slog.Error("list webhook subscriptions failed", "error", err, "organization_id", orgID, "event", name)
			return
		}
		for i := range named {
			if !seen[named[i].ID] {
				seen[named[i].ID] = true
				subs = append(subs, named[i])
			}
		}
	}
	if len(subs) == 0 {
		return
	}

	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("marshal webhook payload failed", "error", err, "organization_id", orgID, "event", event)
		return
	}
	// Stored and sent without the withheld keys (the invitation link).
	body, ok := redactPayload(event, body)
	if !ok {
		slog.Error("webhook payload without its withheld keys could not be built; not delivered", "organization_id", orgID, "event", event)
		return
	}

	// The scope of the event objects is resolved once, and only when a
	// bound subscription or a restricted scope snapshot needs it.
	var objects []ObjectScope
	for i := range subs {
		if subs[i].ServiceAccountID != "" || !subs[i].Scope.orgWide() {
			objects = d.eventObjects(ctx, orgID, event, body)
			break
		}
	}

	for _, sub := range subs {
		if !d.mayDeliver(ctx, orgID, event, objects, &sub) {
			continue
		}
		job := deliveryJob{
			deliveryID: d.nextID(),
			orgID:      orgID,
			event:      event,
			payload:    body,
			maxAttempt: d.opts.MaxAttempts,
			sub:        sub,
		}

		if d.store != nil {
			// The first attempt is claimed by this process like a retry
			// claim (TEC-14): the delivery is stored with a lease, so the
			// retry worker of no replica sends it in parallel, and takes it
			// over only when this process dies before recording the attempt.
			leased := d.now().Add(defaultClaimLease)
			rec, err := d.store.Enqueue(ctx, DeliveryRecord{
				OrganizationID: orgID,
				SubscriptionID: sub.ID,
				Event:          event,
				Payload:        body,
				Status:         StatusPending,
				MaxAttempts:    d.opts.MaxAttempts,
				NextRetryAt:    &leased,
			})
			if err != nil {
				// The delivery could not be persisted; dropping it is safer
				// than an untracked in-memory retry no operator can observe.
				slog.Error("persist webhook delivery failed",
					"error", err, "organization_id", orgID, "event", event, "subscription_id", sub.ID)
				continue
			}
			job.deliveryID = rec.ID
		}

		d.enqueueJob(job)
	}
}

func (d *Dispatcher) enqueueJob(job deliveryJob) {
	select {
	case <-d.stop:
		return
	default:
	}

	select {
	case d.queue <- job:
	case <-d.stop:
	default:
		// The in-process queue is saturated. Persisted deliveries are picked up
		// by the retry worker once their lease ends, so only the memory-only
		// mode needs to block.
		if d.store == nil {
			select {
			case d.queue <- job:
			case <-d.stop:
			}
		}
	}
}

// DeliverOnce performs a single synchronous delivery attempt against the given
// subscription and returns the recorded attempt. It is used by the webhook test
// endpoint, where the caller wants immediate feedback instead of a queued retry
// sequence.
func (d *Dispatcher) DeliverOnce(sub Subscription, event string, payload any) (Delivery, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return Delivery{}, fmt.Errorf("marshal webhook payload: %w", err)
	}

	job := deliveryJob{
		deliveryID: d.nextID(),
		orgID:      sub.OrganizationID,
		event:      event,
		payload:    body,
		maxAttempt: 1,
		sub:        sub,
	}

	statusCode, _, sendErr := d.send(job)
	return d.recordAttempt(job, 1, statusCode, sendErr), nil
}

// Deliveries returns a snapshot of the delivery attempts made by this process.
// The durable history is available through the DeliveryStore.
func (d *Dispatcher) Deliveries() []Delivery {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]Delivery, len(d.deliveries))
	copy(out, d.deliveries)
	return out
}

// ListDeliveries returns the persisted delivery history of a subscription.
func (d *Dispatcher) ListDeliveries(ctx context.Context, orgID, subscriptionID string, page api.PaginationParams) ([]DeliveryRecord, int, error) {
	if d == nil || d.store == nil {
		return nil, 0, ErrNoDeliveryStore
	}
	return d.store.ListBySubscription(ctx, orgID, subscriptionID, page)
}

// ErrNotRetryable reports a delivery that is not failed or dead.
var ErrNotRetryable = errors.New("only a failed or dead delivery can be retried")

// requeuer puts a failed or dead delivery back into the queue (PostgreSQL
// and memory store).
type requeuer interface {
	Requeue(ctx context.Context, orgID, subscriptionID, deliveryID string, now time.Time) (DeliveryRecord, error)
}

// Retry queues a failed or dead delivery for a new round of attempts
// (WHK-01); the retry worker sends it with the next poll.
func (d *Dispatcher) Retry(ctx context.Context, orgID, subscriptionID, deliveryID string) (DeliveryRecord, error) {
	if d == nil || d.store == nil {
		return DeliveryRecord{}, ErrNoDeliveryStore
	}
	r, ok := d.store.(requeuer)
	if !ok {
		return DeliveryRecord{}, ErrNoDeliveryStore
	}
	return r.Requeue(ctx, orgID, subscriptionID, deliveryID, d.now())
}

// ListDeadLetters returns the dead-letter queue of a tenant: the entries of
// the subscriptions visible in ctx (E-98). The PostgreSQL store reads them
// under row level security; for the memory store the repository decides.
func (d *Dispatcher) ListDeadLetters(ctx context.Context, orgID string, page api.PaginationParams) ([]DeadLetter, int, error) {
	if d == nil || d.store == nil {
		return nil, 0, ErrNoDeliveryStore
	}
	if m, ok := d.store.(*MemoryDeliveryStore); ok && d.repo != nil {
		return m.listDeadLetters(orgID, page, func(subscriptionID string) bool {
			_, err := d.repo.GetByID(ctx, orgID, subscriptionID)
			return err == nil
		})
	}
	return d.store.ListDeadLetters(ctx, orgID, page)
}

// Shutdown stops the workers and waits for in-flight deliveries to finish.
func (d *Dispatcher) Shutdown(ctx context.Context) error {
	if d == nil {
		return nil
	}

	d.stopOnce.Do(func() {
		close(d.stop)
		close(d.queue)
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.wg.Wait()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

// retryLoop claims deliveries whose retry time has come and re-sends them.
func (d *Dispatcher) retryLoop(interval time.Duration) {
	// background worker: no request context
	d.RunRetries(context.Background(), interval)
}

// RunRetries runs the retry worker until ctx is cancelled or the dispatcher
// shuts down. A dispatcher created with a negative PollInterval starts no
// retry worker of its own; the server then runs it under its operator
// switch (OPS-02).
func (d *Dispatcher) RunRetries(ctx context.Context, interval time.Duration) {
	if d == nil || d.store == nil {
		return
	}
	if interval <= 0 {
		interval = defaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stop:
			return
		case <-ticker.C:
			d.ProcessDue(ctx)
		}
	}
}

// ProcessDue claims and re-sends every delivery that is due for another
// attempt. It is called by the retry worker and directly by tests.
func (d *Dispatcher) ProcessDue(ctx context.Context) {
	if d == nil || d.store == nil {
		return
	}

	records, err := d.store.ClaimDue(ctx, d.now(), defaultClaimBatch, defaultClaimLease)
	if err != nil {
		observability.ObserveWorkerRun("webhook_dispatcher", 1)
		slog.Error("claim due webhook deliveries failed", "error", err)
		return
	}
	observability.ObserveWorkerRun("webhook_dispatcher", 0)

	for _, rec := range records {
		sub, err := d.repo.GetByID(orgScoped(ctx, rec.OrganizationID), rec.OrganizationID, rec.SubscriptionID)
		if err != nil {
			rec.Attempt++
			d.finalize(ctx, rec, 0, 0, fmt.Errorf("subscription unavailable: %w", err), true)
			continue
		}
		if !sub.IsActive {
			rec.Attempt++
			d.finalize(ctx, rec, 0, 0, fmt.Errorf("subscription is inactive"), true)
			continue
		}

		d.attempt(deliveryJob{
			deliveryID: rec.ID,
			orgID:      rec.OrganizationID,
			event:      rec.Event,
			payload:    rec.Payload,
			attempt:    rec.Attempt,
			maxAttempt: maxAttemptsOrDefault(rec.MaxAttempts),
			sub:        *sub,
		})
	}
}

// attempt performs a single delivery attempt and schedules the next one.
func (d *Dispatcher) attempt(job deliveryJob) {
	statusCode, duration, err := d.send(job)
	attempt := job.attempt + 1
	d.recordAttempt(job, attempt, statusCode, err)

	if d.store == nil {
		if err != nil && attempt < job.maxAttempt {
			select {
			case <-d.stop:
				return
			case <-time.After(d.backoff(attempt)):
			}
			job.attempt = attempt
			d.attempt(job)
		}
		return
	}

	// background worker: no request context
	d.finalize(context.Background(), DeliveryRecord{
		ID:             job.deliveryID,
		OrganizationID: job.orgID,
		SubscriptionID: job.sub.ID,
		Event:          job.event,
		Attempt:        attempt,
		MaxAttempts:    job.maxAttempt,
	}, statusCode, duration, err, attempt >= job.maxAttempt)
}

// finalize persists the outcome of an attempt, scheduling a retry when the
// delivery still has attempts left.
func (d *Dispatcher) finalize(ctx context.Context, rec DeliveryRecord, statusCode int, duration time.Duration, err error, exhausted bool) {
	if d.store == nil {
		return
	}

	now := d.now()
	rec.ResponseStatus = statusCode
	rec.DurationMS = int(duration.Milliseconds())
	rec.MaxAttempts = maxAttemptsOrDefault(rec.MaxAttempts)

	switch {
	case err == nil:
		rec.Status = StatusDelivered
		rec.Error = ""
		rec.DeliveredAt = &now
		rec.NextRetryAt = nil
	case exhausted:
		rec.Status = StatusDead
		rec.Error = err.Error()
		rec.NextRetryAt = nil
	default:
		next := now.Add(d.backoff(rec.Attempt))
		rec.Status = StatusFailed
		rec.Error = err.Error()
		rec.NextRetryAt = &next
	}

	// A delivery that exhausted its retry budget leaves the active queue and
	// is preserved in the dead-letter queue for operator inspection/replay.
	if rec.Status == StatusDead {
		if dlqErr := d.store.MoveToDeadLetter(ctx, rec); dlqErr != nil {
			slog.Error("move webhook delivery to dead-letter queue failed",
				"error", dlqErr, "delivery_id", rec.ID, "organization_id", rec.OrganizationID)
			// Fall back to marking the delivery dead so the outcome is still
			// recorded even though the dead letter could not be written.
			if updateErr := d.store.Update(ctx, rec); updateErr != nil {
				slog.Error("update webhook delivery failed",
					"error", updateErr, "delivery_id", rec.ID, "organization_id", rec.OrganizationID)
			}
		}
		disabled := d.deactivateOnRepeatedDeath(ctx, &rec, now)
		if d.opts.Events != nil {
			if err := d.opts.Events.Emit(ctx, &events.Event{OrgID: rec.OrganizationID, Type: events.WebhookDead, EntityID: rec.SubscriptionID,
				Data: map[string]any{"delivery_id": rec.ID, "subscription_id": rec.SubscriptionID, "event": rec.Event,
					"attempts": rec.Attempt, "last_error": rec.Error, "subscription_disabled": disabled}}); err != nil {
				slog.Error("report dead webhook delivery", "error", err, "delivery_id", rec.ID)
			}
		}
		return
	}

	if updateErr := d.store.Update(ctx, rec); updateErr != nil {
		slog.Error("update webhook delivery failed",
			"error", updateErr, "delivery_id", rec.ID, "organization_id", rec.OrganizationID)
	}
}

// deadCounter counts the dead deliveries of a subscription (PostgreSQL and
// memory store).
type deadCounter interface {
	CountDeadSince(ctx context.Context, orgID, subscriptionID string, since time.Time) (int, error)
}

// deactivator switches a subscription off (PostgreSQL and memory
// repository).
type deactivator interface {
	Deactivate(ctx context.Context, orgID, id string) error
}

// deactivateOnRepeatedDeath switches a subscription off once deadLimit of
// its deliveries died within deadWindow (WHK-01); the webhook.dead event of
// the last one tells the owners (webhook managers, NTF-05). It reports
// whether the subscription was switched off.
func (d *Dispatcher) deactivateOnRepeatedDeath(ctx context.Context, rec *DeliveryRecord, now time.Time) bool {
	counter, ok := d.store.(deadCounter)
	if !ok {
		return false
	}
	off, ok := d.repo.(deactivator)
	if !ok {
		return false
	}
	dead, err := counter.CountDeadSince(ctx, rec.OrganizationID, rec.SubscriptionID, now.Add(-deadWindow))
	if err != nil {
		slog.Error("count dead webhook deliveries", "error", err, "subscription_id", rec.SubscriptionID)
		return false
	}
	if dead < deadLimit {
		return false
	}
	if err := off.Deactivate(ctx, rec.OrganizationID, rec.SubscriptionID); err != nil {
		slog.Error("deactivate webhook subscription", "error", err, "subscription_id", rec.SubscriptionID)
		return false
	}
	slog.Warn("webhook subscription deactivated", "event", "webhook.subscription.disabled",
		"organization_id", rec.OrganizationID, "subscription_id", rec.SubscriptionID, "dead_deliveries", dead)
	return true
}

// backoff returns the delay before the attempt following the given one.
func (d *Dispatcher) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if n := len(d.opts.schedule); n > 0 {
		return d.opts.schedule[min(attempt, n)-1]
	}
	delay := d.opts.BaseBackoff
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= d.opts.MaxBackoff {
			return d.opts.MaxBackoff
		}
	}
	return delay
}

func (d *Dispatcher) send(job deliveryJob) (int, time.Duration, error) {
	start := time.Now()

	req, err := http.NewRequest(http.MethodPost, job.sub.URL, bytes.NewReader(job.payload))
	if err != nil {
		return 0, time.Since(start), err
	}
	req.Header.Set("Content-Type", "application/json")
	signature := signPayload(job.sub.Secret, job.payload)
	// The headers of WHK-01; the X-Webhook-* names stay for subscribers of
	// the earlier releases.
	req.Header.Set("X-Reticora-Event", job.event)
	req.Header.Set("X-Reticora-Delivery", job.deliveryID)
	req.Header.Set("X-Reticora-Signature", signature)
	req.Header.Set("X-Webhook-Event", job.event)
	req.Header.Set("X-Webhook-ID", job.deliveryID)
	req.Header.Set("X-Webhook-Signature", signature)
	// During the overlap after a rotation the old secret signs as well
	// (SEC-06).
	if job.sub.PreviousSecret != "" && job.sub.PreviousSecretExpiresAt != nil && d.now().Before(*job.sub.PreviousSecretExpiresAt) {
		previous := signPayload(job.sub.PreviousSecret, job.payload)
		req.Header.Set("X-Reticora-Signature-Previous", previous)
		req.Header.Set("X-Webhook-Signature-Previous", previous)
	}
	for key, value := range job.sub.Headers {
		req.Header.Set(key, value)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return 0, time.Since(start), err
	}
	defer resp.Body.Close()

	duration := time.Since(start)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, duration, nil
	}
	return resp.StatusCode, duration, fmt.Errorf("unexpected status %d", resp.StatusCode)
}

func (d *Dispatcher) recordAttempt(job deliveryJob, attempt, statusCode int, err error) Delivery {
	delivery := Delivery{
		ID:             job.deliveryID,
		OrganizationID: job.orgID,
		SubscriptionID: job.sub.ID,
		Event:          job.event,
		URL:            job.sub.URL,
		Attempt:        attempt,
		StatusCode:     statusCode,
		Success:        err == nil && statusCode >= 200 && statusCode < 300,
		CreatedAt:      d.now(),
	}
	if err != nil {
		delivery.Error = err.Error()
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.deliveries = append(d.deliveries, delivery)
	return delivery
}

func (d *Dispatcher) nextID() string {
	seq := atomic.AddUint64(&d.seq, 1)
	return fmt.Sprintf("wh_%020d", seq)
}

func signPayload(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
