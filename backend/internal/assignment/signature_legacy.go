package assignment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
)

// SignatureMigrateKind is the system run that takes the legacy signatures
// of migration 000047 over as signature documents (AST-02, E-51, WP-749).
const SignatureMigrateKind = "signature.migrate"

// ActionSignatureMigrated is the audit action of a legacy signature taken
// over (resource stock_movement; changes movement_id, document_id and
// payload_sha256).
const ActionSignatureMigrated = "stock_movement.signature_migrated"

// Reasons a legacy signature is not taken over; the row stays unchanged
// and the next run tries it again.
const (
	LegacySkipForeignKey    = "foreign_object_key"
	LegacySkipMissingImage  = "missing_image"
	LegacySkipNoImage       = "no_image"
	LegacySkipTooLarge      = "image_too_large"
	LegacySkipUnreadable    = "unreadable_blob"
	LegacySkipStorageLimit  = "max_storage_mb"
	LegacySkipNotEntitled   = "documents_not_entitled"
	LegacySkipNoVault       = "plaintext_signer_without_vault"
	LegacySkipNoActor       = "movement_without_user"
	LegacySkipInvalidFormat = "invalid_payload"
)

// legacyBatch is the number of rows per transaction, maxLegacyImage the
// largest image taken over (the default upload limit of the documents).
const (
	legacyBatch    = 100
	maxLegacyImage = config.DefaultDocumentMaxUploadMB << 20
)

// legacyImageTypes are the image types of the upload whitelist
// (document/sniff.go); the content signature decides.
var legacyImageTypes = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

// legacyImageOrder is the order in which the types are tried.
var legacyImageOrder = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

// errLegacyDone: the row was taken over or cleared in the meantime.
var errLegacyDone = errors.New("legacy signature already taken over")

// LegacySignatures is the run signature.migrate (WP-749): every legacy
// signature of assignment_signature_legacy (000266) still holding its
// image becomes a document of the category signature with version 1,
// linked to its movement, and its payload the image-free form
// {document_id, signer_surrogate, signed_at, method}; payload_sha256
// stays as the evidence of the original. stock_movement stays unchanged
// (append-only, signature_hash stays NULL), the verification reports these
// signatures as legacy with valid null.
type LegacySignatures struct {
	pool   *pgxpool.Pool
	blobs  blob.Store
	limits document.StorageLimiter
	scans  document.ScanSubmitter
}

// NewLegacySignatures creates the run on pool; blobs stores the images
// (and holds those behind a legacy object key).
func NewLegacySignatures(pool *pgxpool.Pool, blobs blob.Store) *LegacySignatures {
	return &LegacySignatures{pool: pool, blobs: blobs}
}

// WithLimits counts the images against max_storage_mb (nil: unlimited).
func (l *LegacySignatures) WithLimits(limits document.StorageLimiter) *LegacySignatures {
	l.limits = limits
	return l
}

// WithScans submits the malware scan of each new version after the
// commit (nil: the version stays pending_scan for the sweep).
func (l *LegacySignatures) WithScans(scans document.ScanSubmitter) *LegacySignatures {
	l.scans = scans
	return l
}

// Schedule runs the takeover daily; a later run finds only the rows that
// could not be taken over.
func (l *LegacySignatures) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: SignatureMigrateKind, Spec: "@every 24h", MaxCatchUp: 1, Timeout: time.Hour,
		Run: func(ctx context.Context) error {
			res, err := l.Run(ctx)
			if res.Migrated > 0 || res.Skipped() > 0 {
				slog.InfoContext(ctx, "legacy signatures taken over", "migrated", res.Migrated, "skipped", res.Skipped())
			}
			return err
		}}
}

// LegacyResult counts the rows a run took over and those it left, by
// reason.
type LegacyResult struct {
	Migrated int
	Reasons  map[string]int
}

// Skipped is the number of rows left unchanged.
func (r *LegacyResult) Skipped() int {
	n := 0
	for _, c := range r.Reasons {
		n += c
	}
	return n
}

func (r *LegacyResult) add(o *LegacyResult) {
	r.Migrated += o.Migrated
	for reason, n := range o.Reasons {
		r.skip(reason, n)
	}
}

func (r *LegacyResult) skip(reason string, n int) {
	if r.Reasons == nil {
		r.Reasons = map[string]int{}
	}
	r.Reasons[reason] += n
}

// Run takes the legacy signatures of every organization over.
func (l *LegacySignatures) Run(ctx context.Context) (LegacyResult, error) {
	var total LegacyResult
	orgs, err := database.OrganizationIDs(ctx, l.pool)
	if err != nil {
		return total, err
	}
	for _, org := range orgs {
		res, rerr := l.RunOrg(ctx, org)
		total.add(&res)
		if rerr != nil {
			return total, fmt.Errorf("organization %s: %w", org, rerr)
		}
	}
	return total, nil
}

// legacyRow is a legacy signature on its way into a document.
type legacyRow struct {
	id, movementID, kind, sha256 string
	payload                      []byte
	actor                        string
	// Filled by prepare.
	image                      []byte
	mime, checksum             string
	signedAt, method           any
	surrogate, plainSigner     string
	documentID, versionID, key string
	stored, done               bool
	reason                     string
}

// RunOrg takes the legacy signatures of orgID over, in batches of
// legacyBatch rows: each batch is read in a tenant transaction of the
// whole organization, its images are checked and stored outside a
// transaction and the documents, links, payloads and audit entries are
// written in one transaction (a savepoint per row, so a row over the
// storage limit stays as it is). On an error the batch is rolled back and
// its stored images are deleted.
func (l *LegacySignatures) RunOrg(ctx context.Context, orgID string) (LegacyResult, error) {
	var res LegacyResult
	if l.blobs == nil {
		return res, ErrSignatureUnavailable
	}
	var limit *int64
	var notEntitled, limited bool
	scope := database.OrgWideScope(orgID, "")
	after := "00000000-0000-0000-0000-000000000000"
	for {
		rows, used, rerr := readLegacyBatch(ctx, l.pool, &scope, after)
		if rerr != nil {
			return res, rerr
		}
		if len(rows) == 0 {
			return res, nil
		}
		if !limited {
			// The storage limit only for an organization with legacy rows.
			if limit, notEntitled, rerr = l.storageLimit(ctx, orgID); rerr != nil {
				return res, rerr
			}
			limited = true
		}
		after = rows[len(rows)-1].id
		if berr := l.runBatch(ctx, &scope, rows, used, limit, notEntitled); berr != nil {
			return res, berr
		}
		for _, r := range rows {
			switch {
			case r.done:
				res.Migrated++
			case r.reason != "":
				res.skip(r.reason, 1)
				slog.WarnContext(ctx, "legacy signature not taken over", "organization", orgID, "row", r.id, "reason", r.reason)
			}
		}
		if len(rows) < legacyBatch {
			return res, nil
		}
	}
}

// storageLimit is the storage limit of orgID; an organization without the
// feature documents takes nothing over (notEntitled).
func (l *LegacySignatures) storageLimit(ctx context.Context, orgID string) (limit *int64, notEntitled bool, err error) {
	if l.limits == nil {
		return nil, false, nil
	}
	limit, err = l.limits.StorageLimitBytes(ctx, orgID)
	var refused *entitlement.FeatureNotEntitledError
	if errors.As(err, &refused) {
		return nil, true, nil
	}
	return limit, false, err
}

// readLegacyBatch reads the next rows after the id after that still hold
// their image, with the user that booked the movement (empty when the
// actor is no user of the organization), and the storage in use.
func readLegacyBatch(ctx context.Context, pool *pgxpool.Pool, scope *database.TenantScope, after string) ([]*legacyRow, int64, error) {
	var rows []*legacyRow
	var used int64
	err := database.WithTenant(ctx, pool, scope, func(ctx context.Context, tx pgx.Tx) error {
		found, err := tx.Query(ctx, `SELECT l.id::text, l.movement_id::text, l.kind, l.payload_sha256, l.payload::text,
				COALESCE((SELECT u.id::text FROM app_user u WHERE u.id = m.actor_id AND u.organization_id = l.organization_id), '')
			FROM assignment_signature_legacy l
			JOIN stock_movement m ON m.id = l.movement_id AND m.organization_id = l.organization_id
			WHERE l.organization_id = $1 AND l.id > $2 AND l.payload IS NOT NULL AND NOT l.payload ? 'document_id'
			ORDER BY l.id LIMIT $3`, scope.OrgID, after, legacyBatch)
		if err != nil {
			return fmt.Errorf("read legacy signatures: %w", err)
		}
		rows, err = pgx.CollectRows(found, func(r pgx.CollectableRow) (*legacyRow, error) {
			row := &legacyRow{}
			scanErr := r.Scan(&row.id, &row.movementID, &row.kind, &row.sha256, &row.payload, &row.actor)
			return row, scanErr
		})
		if err != nil {
			return fmt.Errorf("read legacy signatures: %w", err)
		}
		if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT storage_bytes FROM organization_usage WHERE organization_id = $1), 0)`,
			scope.OrgID).Scan(&used); err != nil {
			return fmt.Errorf("read storage usage: %w", err)
		}
		return nil
	})
	return rows, used, err
}

// runBatch prepares the rows, stores their images and writes the batch.
func (l *LegacySignatures) runBatch(ctx context.Context, scope *database.TenantScope, rows []*legacyRow, used int64,
	limit *int64, notEntitled bool) (err error) {
	defer func() {
		for _, r := range rows {
			if r.stored && (err != nil || !r.done) {
				_ = l.blobs.Delete(context.WithoutCancel(ctx), document.Bucket, r.key)
			}
		}
	}()
	var ready []*legacyRow
	for _, r := range rows {
		if notEntitled {
			r.reason = LegacySkipNotEntitled
			continue
		}
		if prepErr := l.prepare(ctx, scope.OrgID, r); prepErr != nil {
			return prepErr
		}
		if r.reason == "" && limit != nil && used+int64(len(r.image)) > *limit {
			r.reason = LegacySkipStorageLimit
		}
		if r.reason != "" {
			continue
		}
		used += int64(len(r.image))
		if err = l.blobs.Put(ctx, document.Bucket, r.key, bytes.NewReader(r.image), int64(len(r.image)), r.mime); err != nil {
			return fmt.Errorf("store legacy signature image: %w", err)
		}
		r.stored = true
		ready = append(ready, r)
	}
	if len(ready) == 0 {
		return nil
	}
	return database.WithTenant(ctx, l.pool, scope, func(ctx context.Context, tx pgx.Tx) error {
		ctx = database.ContextWithTx(ctx, tx)
		for _, r := range ready {
			rowErr := database.Savepoint(ctx, func(ctx context.Context) error {
				return l.migrate(ctx, tx, scope.OrgID, r, limit)
			})
			var exceeded *entitlement.LimitExceededError
			switch {
			case rowErr == nil:
			case errors.Is(rowErr, errLegacyDone):
			case errors.As(rowErr, &exceeded):
				r.reason = LegacySkipStorageLimit
			default:
				return fmt.Errorf("legacy signature %s: %w", r.id, rowErr)
			}
		}
		return nil
	})
}

// prepare reads the payload of r: the image from image_base64 or from the
// blob behind object_key (below the organization's prefix only), its type
// by content signature (an image type of the upload whitelist), the
// signer (a surrogate reference is taken over, a plaintext name sealed
// later, which needs the vault), signed_at and method. A row that cannot
// be taken over gets its reason; only a storage error that is not the
// row's fails.
func (l *LegacySignatures) prepare(ctx context.Context, orgID string, r *legacyRow) error {
	var p map[string]json.RawMessage
	if json.Unmarshal(r.payload, &p) != nil || p == nil {
		r.reason = LegacySkipInvalidFormat
		return nil
	}
	if r.reason = r.readSigner(p["signer_name"]); r.reason != "" {
		return nil
	}
	r.signedAt, r.method = shortString(p["signed_at"]), shortString(p["method"])
	if r.actor == "" {
		r.reason = LegacySkipNoActor
		return nil
	}
	var err error
	r.image, r.reason, err = l.legacyImage(ctx, orgID, p)
	if err != nil || r.reason != "" {
		return err
	}
	for _, mime := range legacyImageOrder {
		if document.VerifyContent(mime, r.image) == nil {
			r.mime = mime
			break
		}
	}
	if r.mime == "" {
		r.reason = LegacySkipNoImage
		return nil
	}
	sum := sha256.Sum256(r.image)
	r.checksum = hex.EncodeToString(sum[:])
	r.documentID, r.versionID = newID(), newID()
	r.key = blob.OrgKey(orgID, "documents", r.documentID, r.versionID)
	return nil
}

// readSigner takes the signer of signer_name: a surrogate reference
// ({"$pii": id}; an empty id is a name redacted without vault) or a
// plaintext name, which needs the vault. It returns the reason of a row
// that cannot be taken over.
func (r *legacyRow) readSigner(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var name string
	if json.Unmarshal(raw, &name) == nil {
		if name = strings.TrimSpace(name); name == "" {
			return ""
		}
		if pii.Default() == nil {
			return LegacySkipNoVault
		}
		r.plainSigner = name
		return ""
	}
	var ref any
	if json.Unmarshal(raw, &ref) != nil {
		return LegacySkipInvalidFormat
	}
	id, ok := pii.RefID(ref)
	if !ok || (id != "" && !api.IsUUID(id)) {
		return LegacySkipInvalidFormat
	}
	r.surrogate = strings.ToLower(id)
	return ""
}

// shortString is a string value of the payload of at most 64 characters,
// else nil (the form the guard of migration 000268 accepts).
func shortString(raw json.RawMessage) any {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil || s == "" || len([]rune(s)) > 64 {
		return nil
	}
	return s
}

// legacyImage returns the image of the payload p and, when there is none
// to take, the reason.
func (l *LegacySignatures) legacyImage(ctx context.Context, orgID string, p map[string]json.RawMessage) (image []byte, reason string, err error) {
	var encoded, key string
	_ = json.Unmarshal(p["image_base64"], &encoded)
	_ = json.Unmarshal(p["object_key"], &key)
	if encoded = strings.TrimSpace(encoded); encoded != "" {
		if i := strings.Index(encoded, ";base64,"); strings.HasPrefix(encoded, "data:") && i > 0 {
			encoded = encoded[i+len(";base64,"):]
		}
		if base64.StdEncoding.DecodedLen(len(encoded)) > maxLegacyImage+3 {
			return nil, LegacySkipTooLarge, nil
		}
		if image, err = base64.StdEncoding.DecodeString(encoded); err != nil {
			return nil, LegacySkipNoImage, nil
		}
		if len(image) > maxLegacyImage {
			return nil, LegacySkipTooLarge, nil
		}
		return image, "", nil
	}
	if key == "" {
		return nil, LegacySkipMissingImage, nil
	}
	if blob.CheckOrgKey(orgID, key) != nil {
		return nil, LegacySkipForeignKey, nil
	}
	rc, err := l.blobs.Get(ctx, document.Bucket, key)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, LegacySkipUnreadable, nil
	}
	image, err = io.ReadAll(io.LimitReader(rc, maxLegacyImage+1))
	_ = rc.Close()
	switch {
	case err != nil && ctx.Err() != nil:
		return nil, "", ctx.Err()
	case err != nil:
		return nil, LegacySkipUnreadable, nil
	case len(image) > maxLegacyImage:
		return nil, LegacySkipTooLarge, nil
	}
	return image, "", nil
}

// migrate writes r in tx (under a savepoint of ctx): the row locked and
// still holding its image (else errLegacyDone), a plaintext signer sealed
// as a vault subject of the kind value, the document of the category
// signature with version 1 (uploaded by the user of the movement, counted
// against limit), its link to the movement, the image-free payload and,
// last before the commit, the audit entry; the scan is submitted after the
// commit.
func (l *LegacySignatures) migrate(ctx context.Context, tx pgx.Tx, orgID string, r *legacyRow, limit *int64) error {
	var open bool
	err := tx.QueryRow(ctx, `SELECT payload IS NOT NULL AND NOT payload ? 'document_id'
		FROM assignment_signature_legacy WHERE id = $1 AND organization_id = $2 FOR UPDATE`, r.id, orgID).Scan(&open)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !open) {
		return errLegacyDone
	}
	if err != nil {
		return fmt.Errorf("lock legacy signature: %w", err)
	}
	surrogate := r.surrogate
	if r.plainSigner != "" {
		ref, sealErr := pii.SealValueTx(ctx, tx, orgID, r.plainSigner)
		if sealErr != nil {
			return fmt.Errorf("seal signer: %w", sealErr)
		}
		surrogate, _ = pii.RefID(ref)
	}
	d := &document.Document{ID: r.documentID, OrganizationID: orgID,
		Title: "Legacy signature " + r.kind + " " + r.movementID, FileName: "signature-" + r.kind + "-" + r.movementID + legacyImageTypes[r.mime],
		Category: document.CategorySignature, UploadedBy: r.actor}
	checksum := r.checksum
	v := &document.Version{ID: r.versionID, StorageKey: r.key, Size: int64(len(r.image)), MimeType: r.mime,
		ChecksumSHA256: &checksum, UploadedBy: r.actor}
	if createErr := document.CreateWithFirstVersionTx(ctx, tx, d, v, limit); createErr != nil {
		return createErr
	}
	if err = document.LinkTx(ctx, tx, &document.DocumentLink{OrganizationID: orgID, DocumentID: r.documentID,
		EntityType: "stock_movement", EntityID: r.movementID}); err != nil {
		return fmt.Errorf("link legacy signature: %w", err)
	}
	var signer any
	if surrogate != "" {
		signer = surrogate
	}
	payload, err := json.Marshal(map[string]any{"document_id": r.documentID, "signer_surrogate": signer,
		"signed_at": r.signedAt, "method": r.method})
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE assignment_signature_legacy SET payload = $2::jsonb WHERE id = $1`, r.id, string(payload)); err != nil {
		return fmt.Errorf("replace legacy signature: %w", err)
	}
	entry := audit.Entry{OrganizationID: orgID, ActorType: "system", Source: "system", Action: ActionSignatureMigrated,
		ResourceType: "stock_movement", ResourceID: r.movementID,
		Changes: map[string]any{"movement_id": r.movementID, "document_id": r.documentID, "payload_sha256": r.sha256}}
	if hookErr := database.BeforeCommit(ctx, func() error {
		if _, recErr := audit.NewPGRecorder().Record(ctx, tx, entry); recErr != nil {
			return fmt.Errorf("audit %s: %w", ActionSignatureMigrated, recErr)
		}
		return nil
	}); hookErr != nil {
		return hookErr
	}
	document.EnqueueScan(ctx, l.scans, v)
	r.done = true
	return nil
}
