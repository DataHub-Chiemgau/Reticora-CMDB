package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

// InAppNotification is one entry of a user's notification center (UI-16).
type InAppNotification struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	UserID         string         `json:"user_id"`
	Category       string         `json:"category"`
	Subject        string         `json:"subject"`
	Body           string         `json:"body"`
	Data           map[string]any `json:"data,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	ReadAt         *time.Time     `json:"read_at,omitempty"`
}

// Inbox stores in-app notifications per user; the database inbox comes
// with the notification table (WP-139).
type Inbox interface {
	Add(ctx context.Context, items []InAppNotification) error
}

// MemoryInbox keeps in-app notifications in memory.
type MemoryInbox struct {
	mu    sync.Mutex
	items []InAppNotification
	seq   int
}

// NewMemoryInbox creates an empty inbox.
func NewMemoryInbox() *MemoryInbox { return &MemoryInbox{} }

// Add implements Inbox.
func (b *MemoryInbox) Add(_ context.Context, items []InAppNotification) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range items {
		b.seq++
		items[i].ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", b.seq)
		b.items = append(b.items, items[i])
	}
	return nil
}

// For returns the notifications of a user, newest first.
func (b *MemoryInbox) For(orgID, userID string) []InAppNotification {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []InAppNotification
	for i := range b.items {
		if b.items[i].OrganizationID == orgID && b.items[i].UserID == userID {
			out = append(out, b.items[i])
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// InAppProvider writes in-app notifications for the recipients that are
// users.
type InAppProvider struct {
	inbox Inbox
}

// NewInAppProvider creates the provider on inbox.
func NewInAppProvider(inbox Inbox) *InAppProvider { return &InAppProvider{inbox: inbox} }

// Send implements Provider.
func (p *InAppProvider) Send(ctx context.Context, ch *Channel, msg *Message) (*Delivery, error) {
	now := time.Now().UTC()
	var items []InAppNotification
	for _, r := range msg.To {
		if r.UserID == "" {
			continue
		}
		items = append(items, InAppNotification{OrganizationID: ch.OrganizationID, UserID: r.UserID, Category: msg.Category,
			Subject: msg.Subject, Body: msg.Body, Data: msg.Data, CreatedAt: now})
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no recipient is a user")
	}
	if err := p.inbox.Add(ctx, items); err != nil {
		return nil, err
	}
	return &Delivery{Recipients: len(items), SentAt: now}, nil
}

// WebhookProvider posts the message as JSON to the channel's URL, signed
// with HMAC-SHA256 over the body when the channel has a secret. The client
// is the egress client of the installation (SEC-08).
type WebhookProvider struct {
	client *http.Client
}

// NewWebhookProvider creates the provider on client.
func NewWebhookProvider(client *http.Client) *WebhookProvider {
	return &WebhookProvider{client: client}
}

// Send implements Provider.
func (p *WebhookProvider) Send(ctx context.Context, ch *Channel, msg *Message) (*Delivery, error) {
	url, _ := ch.Config["url"].(string)
	body, err := json.Marshal(map[string]any{
		"organization_id": ch.OrganizationID, "category": msg.Category, "subject": msg.Subject,
		"body": msg.Body, "recipients": msg.To, "data": msg.Data,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if secret, _ := ch.Config["secret"].(string); secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set("X-Reticora-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("webhook answered %d", resp.StatusCode)
	}
	return &Delivery{Recipients: len(msg.To), SentAt: time.Now().UTC()}, nil
}

// InboxStore is the inbox with the reads of the notification center.
type InboxStore interface {
	Inbox
	List(ctx context.Context, orgID, userID string, unreadOnly bool, limit, offset int) ([]InAppNotification, int, error)
	MarkRead(ctx context.Context, orgID, userID, id string) error
	MarkAllRead(ctx context.Context, orgID, userID string) (int, error)
}

// List implements InboxStore.
func (b *MemoryInbox) List(_ context.Context, orgID, userID string, unreadOnly bool, limit, offset int) ([]InAppNotification, int, error) {
	all := b.For(orgID, userID)
	var out []InAppNotification
	for i := range all {
		if !unreadOnly || all[i].ReadAt == nil {
			out = append(out, all[i])
		}
	}
	total := len(out)
	start := min(offset, total)
	end := total
	if limit > 0 {
		end = min(start+limit, total)
	}
	return out[start:end], total, nil
}

// MarkRead implements InboxStore.
func (b *MemoryInbox) MarkRead(_ context.Context, orgID, userID, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.items {
		n := &b.items[i]
		if n.ID == id && n.OrganizationID == orgID && n.UserID == userID {
			if n.ReadAt == nil {
				now := time.Now().UTC()
				n.ReadAt = &now
			}
			return nil
		}
	}
	return ErrNotFound
}

// MarkAllRead implements InboxStore.
func (b *MemoryInbox) MarkAllRead(_ context.Context, orgID, userID string) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now().UTC()
	n := 0
	for i := range b.items {
		item := &b.items[i]
		if item.OrganizationID == orgID && item.UserID == userID && item.ReadAt == nil {
			item.ReadAt = &now
			n++
		}
	}
	return n, nil
}
