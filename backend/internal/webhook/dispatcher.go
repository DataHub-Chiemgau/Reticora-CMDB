package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
)

const (
	defaultMaxAttempts  = 5
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
	// PollInterval controls how often the retry worker looks for due
	// deliveries. A negative value disables the worker.
	PollInterval time.Duration
	// Access decides for a subscription bound to a service account whether
	// the account may read the event's object (RBA-08). Without it, bound
	// subscriptions receive nothing (fail-closed).
	Access SubscriberAccess
}

// SubscriberAccess checks the read right of a service account on an object
// with the given permission in its client and site (empty: org-wide).
type SubscriberAccess interface {
	CanReadEvent(ctx context.Context, orgID, serviceAccountID, permission, clientID, siteID string) (bool, error)
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

// eventObjectScope reads client_id and site_id of the event object, if the
// payload carries them.
func eventObjectScope(body []byte) (clientID, siteID string) {
	var object struct {
		ClientID *string `json:"client_id"`
		SiteID   *string `json:"site_id"`
	}
	if json.Unmarshal(body, &object) != nil {
		return "", ""
	}
	if object.ClientID != nil {
		clientID = *object.ClientID
	}
	if object.SiteID != nil {
		siteID = *object.SiteID
	}
	return clientID, siteID
}

// mayDeliver reports whether a subscription receives the event: unbound
// subscriptions receive every event of the organization, bound ones only
// events whose object their service account may read.
func (d *Dispatcher) mayDeliver(ctx context.Context, orgID, event string, body []byte, sub *Subscription) bool {
	if sub.ServiceAccountID == "" {
		return true
	}
	permission := EventReadPermission(event)
	if permission == "" || d.opts.Access == nil {
		return false
	}
	clientID, siteID := eventObjectScope(body)
	ok, err := d.opts.Access.CanReadEvent(ctx, orgID, sub.ServiceAccountID, permission, clientID, siteID)
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
	if d == nil || d.repo == nil {
		return
	}

	// Events reach every subscription of the organization, independent of
	// the acting principal's client scope: the lookup runs org-wide on behalf
	// of the organization (E-08; WP-022 moves the dispatcher to a system
	// principal per organization).
	subs, err := d.repo.ListByEvent(orgScoped(ctx, orgID), orgID, event)
	if err != nil {
		slog.Error("list webhook subscriptions failed", "error", err, "organization_id", orgID, "event", event)
		return
	}
	if len(subs) == 0 {
		return
	}

	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("marshal webhook payload failed", "error", err, "organization_id", orgID, "event", event)
		return
	}

	for _, sub := range subs {
		if !d.mayDeliver(ctx, orgID, event, body, &sub) {
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
			now := d.now()
			rec, err := d.store.Enqueue(ctx, DeliveryRecord{
				OrganizationID: orgID,
				SubscriptionID: sub.ID,
				Event:          event,
				Payload:        body,
				Status:         StatusPending,
				MaxAttempts:    d.opts.MaxAttempts,
				NextRetryAt:    &now,
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
		// by the retry worker, so only the memory-only mode needs to block.
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

// ListDeadLetters returns the dead-letter queue of a tenant.
func (d *Dispatcher) ListDeadLetters(ctx context.Context, orgID string, page api.PaginationParams) ([]DeadLetter, int, error) {
	if d == nil || d.store == nil {
		return nil, 0, ErrNoDeliveryStore
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
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-d.stop:
			return
		case <-ticker.C:
			// background worker: no request context
			d.ProcessDue(context.Background())
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
		slog.Error("claim due webhook deliveries failed", "error", err)
		return
	}

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
		rec.Status = StatusSuccess
		rec.Error = ""
		rec.DeliveredAt = &now
		rec.NextRetryAt = nil
	case exhausted:
		rec.Status = StatusFailed
		rec.Error = err.Error()
		rec.NextRetryAt = nil
	default:
		next := now.Add(d.backoff(rec.Attempt))
		rec.Status = StatusRetrying
		rec.Error = err.Error()
		rec.NextRetryAt = &next
	}

	// A delivery that exhausted its retry budget leaves the active queue and
	// is preserved in the dead-letter queue for operator inspection/replay.
	if rec.Status == StatusFailed {
		if dlqErr := d.store.MoveToDeadLetter(ctx, rec); dlqErr != nil {
			slog.Error("move webhook delivery to dead-letter queue failed",
				"error", dlqErr, "delivery_id", rec.ID, "organization_id", rec.OrganizationID)
			// Fall back to marking the delivery failed so the outcome is
			// still recorded even though the dead letter could not be written.
			if updateErr := d.store.Update(ctx, rec); updateErr != nil {
				slog.Error("update webhook delivery failed",
					"error", updateErr, "delivery_id", rec.ID, "organization_id", rec.OrganizationID)
			}
		}
		return
	}

	if updateErr := d.store.Update(ctx, rec); updateErr != nil {
		slog.Error("update webhook delivery failed",
			"error", updateErr, "delivery_id", rec.ID, "organization_id", rec.OrganizationID)
	}
}

// backoff returns the delay before the attempt following the given one.
func (d *Dispatcher) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
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
	req.Header.Set("X-Webhook-Event", job.event)
	req.Header.Set("X-Webhook-ID", job.deliveryID)
	req.Header.Set("X-Webhook-Signature", signPayload(job.sub.Secret, job.payload))
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
