package tenantapi_test

// WP-1156 (TLC-06, E-166): the deletion protocol. The request keeps the
// requester's address encrypted (own data key, wrapped by the master key)
// in org_deletion_notice; the purge writes org.purged with times, counts
// and hashes per table and object kind and a reproducible total hash as
// its last entry before purged; the protocol goes over the central SMTP
// server to the address in the requester's language, after which the
// address is gone; without SMTP the address is deleted and
// org.deletion.protocol_undelivered recorded. tests/org-purge-check.sql
// finds rows left of purged organizations.
//
// Retest: cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestPurgeProtocol|TestOrgPurgeCheckScript' ./internal/tenantapi/

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
)

// fakeSMTP is a minimal SMTP server that records one mail per session
// (pattern of notify/channel_test.go); failing answers DATA with 451,
// rejectRcpt answers RCPT TO with 550.
type fakeSMTP struct {
	ln         net.Listener
	mu         sync.Mutex
	rcpt       [][]string
	data       []string
	failing    bool
	rejectRcpt bool
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSMTP{ln: ln}
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go s.session(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeSMTP) session(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	reply := func(line string) { _, _ = io.WriteString(conn, line+"\r\n") }
	reply("220 fake ESMTP")
	var rcpts []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"):
			reply("250 fake")
		case strings.HasPrefix(upper, "RCPT TO:"):
			s.mu.Lock()
			reject := s.rejectRcpt
			s.mu.Unlock()
			if reject {
				reply("550 mailbox unavailable")
				continue
			}
			rcpts = append(rcpts, strings.Trim(cmd[len("RCPT TO:"):], "<> "))
			reply("250 ok")
		case upper == "DATA":
			s.mu.Lock()
			failing := s.failing
			s.mu.Unlock()
			if failing {
				reply("451 try again later")
				continue
			}
			reply("354 go ahead")
			var b strings.Builder
			for {
				l, rerr := r.ReadString('\n')
				if rerr != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			s.mu.Lock()
			s.rcpt = append(s.rcpt, rcpts)
			s.data = append(s.data, b.String())
			s.mu.Unlock()
			rcpts = nil
			reply("250 queued")
		case upper == "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

func (s *fakeSMTP) mails() ([][]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]string(nil), s.rcpt...), append([]string(nil), s.data...)
}

func (s *fakeSMTP) mailer(t *testing.T) notify.OrgPurgeProtocolMailer {
	t.Helper()
	sender, err := notify.NewSMTPSender(&notify.SMTPConfig{Host: "127.0.0.1", Port: s.ln.Addr().(*net.TCPAddr).Port,
		From: "noreply@reticora.test", TLS: notify.SMTPNone})
	if err != nil {
		t.Fatal(err)
	}
	return notify.OrgPurgeProtocolMailer{Sender: sender}
}

// noticeKey sets a fresh notice encryptor for the test.
func noticeKey(t *testing.T) *crypto.EnvelopeEncryptor {
	t.Helper()
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	tenantapi.SetNoticeEncryptor(enc)
	t.Cleanup(func() { tenantapi.SetNoticeEncryptor(nil) })
	return enc
}

// storeNotice puts the encrypted address of org into org_deletion_notice
// as the request does.
func (e *purgeEnv) storeNotice(t *testing.T, enc *crypto.EnvelopeEncryptor, org, address, locale string) {
	t.Helper()
	dek, err := crypto.GenerateDEK()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := crypto.Encrypt(dek, []byte(address))
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := enc.WrapDEK(dek)
	if err != nil {
		t.Fatal(err)
	}
	e.exec(t, `DELETE FROM org_deletion_notice WHERE target_org = $1`, org)
	e.exec(t, `INSERT INTO org_deletion_notice (target_org, recipient_enc, wrapped_dek, locale) VALUES ($1, $2, $3, $4)`,
		org, sealed, wrapped, locale)
	t.Cleanup(func() {
		_, _ = e.f.Admin.Exec(context.Background(), `DELETE FROM org_deletion_notice WHERE target_org = $1`, org)
	})
}

func (e *purgeEnv) notices(t *testing.T, org string) int {
	t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM org_deletion_notice WHERE target_org = $1`, org).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// operatorEntry returns the id and details of the newest operator_audit
// entry of action for org; id 0 without one.
func (e *purgeEnv) operatorEntry(t *testing.T, org, action string) (int64, map[string]any) {
	t.Helper()
	var id int64
	var details map[string]any
	err := e.f.Admin.QueryRow(context.Background(), `SELECT id, details FROM operator_audit WHERE target_org = $1 AND action = $2
		ORDER BY id DESC LIMIT 1`, org, action).Scan(&id, &details)
	if err == pgx.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return id, details
}

func TestPurgeProtocolContent(t *testing.T) {
	e := newExternalEnv(t)
	bg := context.Background()
	results, err := e.purger().PurgeOrganization(bg, e.f.OrgA)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	protocolID, details := e.operatorEntry(t, e.f.OrgA, tenantapi.ActionOrgPurged)
	if protocolID == 0 {
		t.Fatal("no org.purged entry")
	}
	dbPurged, dbDetails := e.operatorEntry(t, e.f.OrgA, "org.deletion.db_purged")
	external, externalDetails := e.operatorEntry(t, e.f.OrgA, "org.deletion.external_purged")
	if external >= protocolID || protocolID >= dbPurged {
		t.Errorf("order of the entries: external %d, protocol %d, db_purged %d; want the protocol after the external purge and before purged",
			external, protocolID, dbPurged)
	}
	raw, _ := json.Marshal(details)
	var protocol tenantapi.PurgeProtocol
	if err = json.Unmarshal(raw, &protocol); err != nil {
		t.Fatal(err)
	}
	if protocol.RequestedAt == "" || protocol.DueAt == "" || protocol.PurgeStarted == "" || protocol.PurgeEnded == "" {
		t.Errorf("times of the protocol %+v", protocol)
	}
	if protocol.PurgeStarted > protocol.PurgeEnded {
		t.Errorf("purge started %s after it ended %s", protocol.PurgeStarted, protocol.PurgeEnded)
	}
	if len(protocol.Tables) != len(results) {
		t.Fatalf("protocol has %d tables, the purge %d", len(protocol.Tables), len(results))
	}
	byTable := map[string]tenantapi.PurgeTableResult{}
	for _, r := range results {
		byTable[r.Table] = r
	}
	var rows int64
	for _, r := range protocol.Tables {
		if want := byTable[r.Table]; want.SHA256 != r.SHA256 || want.RowsBefore != r.RowsBefore || len(r.SHA256) != 64 {
			t.Errorf("protocol table %+v, purge %+v", r, want)
		}
		rows += r.RowsBefore
	}
	if protocol.Rows != rows || rows == 0 {
		t.Errorf("protocol rows %d, sum of the tables %d", protocol.Rows, rows)
	}
	// Counts and hashes per table are those of org.deletion.db_purged
	// (WP-1154).
	var db struct {
		Tables []tenantapi.PurgeTableResult `json:"tables"`
		Rows   int64                        `json:"rows"`
	}
	dbRaw, _ := json.Marshal(dbDetails)
	if err = json.Unmarshal(dbRaw, &db); err != nil {
		t.Fatal(err)
	}
	if db.Rows != protocol.Rows || len(db.Tables) != len(protocol.Tables) {
		t.Fatalf("db_purged rows %d over %d tables, protocol %d over %d", db.Rows, len(db.Tables), protocol.Rows, len(protocol.Tables))
	}
	dbByTable := map[string]tenantapi.PurgeTableResult{}
	for _, r := range db.Tables {
		dbByTable[r.Table] = r
	}
	for _, r := range protocol.Tables {
		if want, ok := dbByTable[r.Table]; !ok || want.SHA256 != r.SHA256 || want.RowsBefore != r.RowsBefore {
			t.Errorf("protocol table %+v, db_purged %+v", r, want)
		}
	}
	// The object counts are those of org.deletion.external_purged.
	wantSteps, _ := json.Marshal(externalDetails["steps"])
	gotSteps, _ := json.Marshal(protocol.Objects)
	if string(wantSteps) != string(gotSteps) || protocol.ObjectsTotal == 0 {
		t.Errorf("objects %s (total %d), external_purged %s", gotSteps, protocol.ObjectsTotal, wantSteps)
	}
	if !strings.Contains(protocol.Backups, "retention") {
		t.Errorf("backup notice %q", protocol.Backups)
	}
	// The total hash is reproducible from the protocol.
	if sum, herr := tenantapi.ProtocolHash(&protocol); herr != nil || sum != protocol.SHA256 || len(sum) != 64 {
		t.Errorf("ProtocolHash = %s (%v), recorded %s", sum, herr, protocol.SHA256)
	}
	protocol.Rows++
	if sum, _ := tenantapi.ProtocolHash(&protocol); sum == protocol.SHA256 {
		t.Error("the hash does not change with the protocol")
	}
	// No personal value: the requester appears only as id, never as name or address.
	if strings.Contains(string(raw), "@") {
		t.Errorf("the protocol holds an address: %s", raw)
	}
	// A further run writes no second protocol.
	if _, err = e.purger().PurgeOrganization(bg, e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	if again, _ := e.operatorEntry(t, e.f.OrgA, tenantapi.ActionOrgPurged); again != protocolID {
		t.Errorf("second protocol entry %d, want only %d", again, protocolID)
	}
}

func TestPurgeProtocolMailSent(t *testing.T) {
	e := newExternalEnv(t)
	enc := noticeKey(t)
	e.storeNotice(t, enc, e.f.OrgA, "requester@example.org", "en-US")
	smtp := newFakeSMTP(t)
	if _, err := e.purger().WithProtocolMailer(smtp.mailer(t)).PurgeOrganization(context.Background(), e.f.OrgA); err != nil {
		t.Fatalf("purge: %v", err)
	}
	rcpts, data := smtp.mails()
	if len(data) != 1 || len(rcpts[0]) != 1 || rcpts[0][0] != "requester@example.org" {
		t.Fatalf("mails to %v, want one to the requester", rcpts)
	}
	protocolID, details := e.operatorEntry(t, e.f.OrgA, tenantapi.ActionOrgPurged)
	mail := data[0]
	if !strings.Contains(mail, "Subject: Reticora: deletion protocol of your organization") {
		t.Errorf("not the English protocol:\n%s", mail)
	}
	body := decodeParts(t, mail)
	if !strings.Contains(body, details["sha256"].(string)) || !strings.Contains(body, "operator_audit") ||
		!strings.Contains(body, " "+itoa(protocolID)) {
		t.Errorf("the mail names neither the total hash nor the entry %d:\n%s", protocolID, body)
	}
	if !strings.Contains(mail, "deletion-protocol.txt") || !strings.Contains(body, "table\trows_before\tdeleted\tsha256") {
		t.Errorf("no attachment with the table counts:\n%s", mail)
	}
	if n := e.notices(t, e.f.OrgA); n != 0 {
		t.Errorf("%d stored addresses after the protocol was sent", n)
	}
}

func TestPurgeProtocolAddressDestroyed(t *testing.T) {
	enc := noticeKey(t)
	d := newDeletionEnv(t)
	env := &purgeEnv{f: d.f}
	var email string
	if err := d.f.Admin.QueryRow(context.Background(), `SELECT email FROM app_user WHERE id = $1`, d.admin).Scan(&email); err != nil {
		t.Fatal(err)
	}
	// The request keeps the address encrypted; the cancel deletes it.
	if r := d.call(t, "admin-mfa", http.MethodPost, `{"confirm_slug":"`+d.slug+`"}`); r.code != http.StatusAccepted {
		t.Fatalf("request: %d %s", r.code, r.body)
	}
	t.Cleanup(func() {
		_, _ = d.f.Admin.Exec(context.Background(), `UPDATE organization SET deletion_state = 'none', deletion_requested_at = NULL,
			deletion_due_at = NULL, deletion_requested_by = NULL WHERE id = $1`, d.f.OrgA)
	})
	var sealed, wrapped []byte
	var locale string
	if err := d.f.Admin.QueryRow(context.Background(), `SELECT recipient_enc, wrapped_dek, locale FROM org_deletion_notice WHERE target_org = $1`,
		d.f.OrgA).Scan(&sealed, &wrapped, &locale); err != nil {
		t.Fatalf("no stored address after the request: %v", err)
	}
	if strings.Contains(string(sealed), email) || strings.Contains(string(wrapped), email) || locale == "" {
		t.Errorf("the address is stored in plaintext or without language")
	}
	dek, err := enc.UnwrapDEK(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if plain, derr := crypto.Decrypt(dek, sealed); derr != nil || string(plain) != email {
		t.Errorf("decrypted address %q (%v), want %s", plain, derr, email)
	}
	// Tenant transactions see no row of the operator table.
	var visible int
	if err = d.f.App.QueryRow(orgCtx(d.f.OrgA), `SELECT count(*) FROM org_deletion_notice`).Scan(&visible); err == nil && visible != 0 {
		t.Errorf("the application role sees %d notices outside the operator context", visible)
	}
	if r := d.call(t, "admin-mfa", http.MethodDelete, ""); r.code != http.StatusOK {
		t.Fatalf("cancel: %d %s", r.code, r.body)
	}
	if n := env.notices(t, d.f.OrgA); n != 0 {
		t.Errorf("%d stored addresses after the cancel", n)
	}

	// A request of a requester with English as language: the purge sends
	// the protocol to the requester's address in English, then the address
	// is gone.
	env.exec(t, `UPDATE app_user SET locale = 'en' WHERE id = $1`, d.admin)
	if r := d.call(t, "admin-mfa", http.MethodPost, `{"confirm_slug":"`+d.slug+`"}`); r.code != http.StatusAccepted {
		t.Fatalf("second request: %d %s", r.code, r.body)
	}
	if err = d.f.Admin.QueryRow(context.Background(), `SELECT locale FROM org_deletion_notice WHERE target_org = $1`,
		d.f.OrgA).Scan(&locale); err != nil || locale != "en" {
		t.Errorf("stored language %q (%v), want the requester's en", locale, err)
	}
	// The grace period is over (the trigger allows no other change of a
	// request than its cancel, so the request is put back with an ended
	// grace period; the stored address stays).
	env.exec(t, `UPDATE organization SET deletion_state = 'none', deletion_requested_at = NULL, deletion_due_at = NULL,
		deletion_requested_by = NULL WHERE id = $1`, d.f.OrgA)
	env.user = d.admin
	env.requestDue(t)
	smtp := newFakeSMTP(t)
	if _, err = tenantapi.NewPurger(d.f.App, purgeTables).WithProtocolMailer(smtp.mailer(t)).
		PurgeOrganization(context.Background(), d.f.OrgA); err != nil {
		t.Fatalf("purge: %v", err)
	}
	rcpts, data := smtp.mails()
	if len(data) != 1 || len(rcpts[0]) != 1 || rcpts[0][0] != email {
		t.Fatalf("mails to %v, want one to the requester %s", rcpts, email)
	}
	if !strings.Contains(data[0], "Subject: Reticora: deletion protocol of your organization") {
		t.Errorf("not the English protocol:\n%s", data[0])
	}
	if n := env.notices(t, d.f.OrgA); n != 0 {
		t.Errorf("%d stored addresses after the protocol was sent", n)
	}
}

func TestPurgeProtocolUndelivered(t *testing.T) {
	e := newExternalEnv(t)
	enc := noticeKey(t)
	e.storeNotice(t, enc, e.f.OrgA, "requester@example.org", "de-DE")
	// The central SMTP server is not configured.
	purger := e.purger().WithProtocolMailer(notify.OrgPurgeProtocolMailer{})
	if _, err := purger.PurgeOrganization(context.Background(), e.f.OrgA); err != nil {
		t.Fatalf("purge: %v", err)
	}
	var state string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT deletion_state FROM organization WHERE id = $1`, e.f.OrgA).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != tenantapi.DeletionPurged {
		t.Errorf("state %s without SMTP, want purged", state)
	}
	id, details := e.operatorEntry(t, e.f.OrgA, tenantapi.ActionProtocolUndelivered)
	if id == 0 || details["reason"] != "no_smtp" {
		t.Errorf("protocol_undelivered %d %v, want reason no_smtp", id, details)
	}
	if n := e.notices(t, e.f.OrgA); n != 0 {
		t.Errorf("%d stored addresses after giving up", n)
	}

	// A failing server is tried three times, then given up.
	e2 := &purgeEnv{f: e.f}
	e2.storeNotice(t, enc, e.f.OrgA, "requester@example.org", "de-DE")
	smtp := newFakeSMTP(t)
	smtp.failing = true
	purger = e.purger().WithProtocolMailer(smtp.mailer(t))
	for attempt := 1; attempt <= 3; attempt++ {
		if err := purger.DeliverProtocol(context.Background(), e.f.OrgA); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		want := 1
		if attempt == 3 {
			want = 0
		}
		if n := e.notices(t, e.f.OrgA); n != want {
			t.Errorf("after attempt %d: %d stored addresses, want %d", attempt, n, want)
		}
	}
	if _, details = e.operatorEntry(t, e.f.OrgA, tenantapi.ActionProtocolUndelivered); details["reason"] != "attempts" {
		t.Errorf("reason after three failures %v, want attempts", details["reason"])
	}
}

// TestPurgeProtocolSendErrorLogsNoAddress: a server that rejects the
// recipient leaves only the SMTP reply code in the log, never the address
// of the requester (it must not outlive the purge).
func TestPurgeProtocolSendErrorLogsNoAddress(t *testing.T) {
	e := newExternalEnv(t)
	enc := noticeKey(t)
	const address = "secret-requester@example.org"
	e.storeNotice(t, enc, e.f.OrgA, address, "de-DE")
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&syncWriter{w: &logs}, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	smtp := newFakeSMTP(t)
	smtp.rejectRcpt = true
	if _, err := e.purger().WithProtocolMailer(smtp.mailer(t)).PurgeOrganization(context.Background(), e.f.OrgA); err != nil {
		t.Fatalf("purge: %v", err)
	}
	out := logs.String()
	if !strings.Contains(out, "smtp reply 550") {
		t.Errorf("the log does not name the reply code:\n%s", out)
	}
	if strings.Contains(out, address) || strings.Contains(out, "secret-requester") {
		t.Errorf("the log holds the address of the requester:\n%s", out)
	}
}

// TestPurgeProtocolOneAttemptPerRun: a run that purges an organization
// spends one attempt on its protocol; the retry belongs to the next run.
func TestPurgeProtocolOneAttemptPerRun(t *testing.T) {
	e := newExternalEnv(t)
	enc := noticeKey(t)
	e.storeNotice(t, enc, e.f.OrgA, "requester@example.org", "de-DE")
	smtp := newFakeSMTP(t)
	smtp.failing = true
	purger := e.purger().WithProtocolMailer(smtp.mailer(t))
	_ = purger.Run(context.Background())
	attempts := func() int {
		var n int
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT attempts FROM org_deletion_notice WHERE target_org = $1`,
			e.f.OrgA).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := attempts(); n != 1 {
		t.Errorf("attempts after the purging run %d, want 1", n)
	}
	_ = purger.Run(context.Background())
	if n := attempts(); n != 2 {
		t.Errorf("attempts after the next run %d, want 2", n)
	}
}

// syncWriter serializes the writes of the log handler.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// checkScript runs tests/org-purge-check.sql like psql: the query before
// \gexec builds one statement per table, each statement prints its rows.
func checkScript(t *testing.T, e *purgeEnv) []string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "tests", "org-purge-check.sql"))
	if err != nil {
		t.Fatal(err)
	}
	var query []string
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == `\gexec` {
			break
		}
		if strings.HasPrefix(trimmed, "--") || strings.HasPrefix(trimmed, `\`) {
			continue
		}
		query = append(query, line)
	}
	ctx := context.Background()
	rows, err := e.f.Admin.Query(ctx, strings.Join(query, "\n"))
	if err != nil {
		t.Fatalf("generator: %v", err)
	}
	statements, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(statements) < 100 {
		t.Fatalf("the script checks only %d tables", len(statements))
	}
	var out []string
	for _, stmt := range statements {
		var table string
		var n int64
		qerr := e.f.Admin.QueryRow(ctx, stmt).Scan(&table, &n)
		if qerr == pgx.ErrNoRows {
			continue
		}
		if qerr != nil {
			t.Fatalf("%s: %v", stmt, qerr)
		}
		out = append(out, table)
	}
	return out
}

func TestOrgPurgeCheckScriptEmpty(t *testing.T) {
	e := newExternalEnv(t)
	if _, err := e.purger().PurgeOrganization(context.Background(), e.f.OrgA); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if left := checkScript(t, e.purgeEnv); len(left) != 0 {
		t.Errorf("rows left of purged organizations in %v", left)
	}
	// A row left on purpose appears.
	e.exec(t, `INSERT INTO notice_sent (organization_id, kind, dedup_key, sent_at) VALUES ($1, 'wp1156', 'left', now())`, e.f.OrgA)
	t.Cleanup(func() {
		_, _ = e.f.Admin.Exec(context.Background(), `DELETE FROM notice_sent WHERE organization_id = $1 AND kind = 'wp1156'`, e.f.OrgA)
	})
	if left := checkScript(t, e.purgeEnv); len(left) != 1 || left[0] != "notice_sent" {
		t.Errorf("script output %v, want notice_sent", left)
	}
}

// decodeParts returns the mail with its quoted-printable and base64 parts
// decoded, for text checks.
func decodeParts(t *testing.T, mail string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(mail, "=\r\n", ""), "=3D", "="))
	for _, part := range strings.Split(mail, "\r\n\r\n") {
		chunk := strings.Join(strings.Fields(strings.Split(part, "\r\n--")[0]), "")
		if decoded, err := base64.StdEncoding.DecodeString(chunk); err == nil && len(decoded) > 0 {
			b.WriteString("\n")
			b.Write(decoded)
		}
	}
	return b.String()
}

func itoa(n int64) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}
