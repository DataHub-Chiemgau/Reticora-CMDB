package user

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
)

// The mobile number of a user (NTF-02, NTF-03, WP-1140, E-164) is a PII
// vault subject of kind user with the data {"mobile": "+49…"}; app_user
// keeps only its surrogate (mobile_pii_subject_id). Every change stores the
// number in a new subject and deletes the old one, so a number once
// replaced or cleared is shredded: neither the API nor a reference in the
// audit resolves it anymore. Responses carry the masked number
// (mobile_masked); the plaintext appears only in the data export.

// mobileField is the key of the number in the vault subject.
const mobileField = "mobile"

// e164 is a normalized E.164 number: +, a country code that does not start
// with 0 and 7 to 15 digits in all.
var e164 = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)

var (
	// ErrMobileInvalid: the number is not in E.164 (422 field mobile).
	ErrMobileInvalid = errors.New("mobile must be empty or an E.164 number such as +4915112345689")
	// ErrNoVault: the server has no PII vault, a number cannot be stored
	// (503).
	ErrNoVault = errors.New("the PII vault is not configured; a mobile number cannot be stored")
)

// NormalizeMobile removes blanks and hyphens and checks E.164; an empty
// value clears the number.
func NormalizeMobile(raw string) (string, error) {
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '\t', ' ':
			return -1
		}
		return r
	}, raw)
	if cleaned == "" {
		return "", nil
	}
	if !e164.MatchString(cleaned) {
		return "", ErrMobileInvalid
	}
	return cleaned, nil
}

// countryCodeLength is the length of the ITU country code an E.164 number
// (without +) starts with: 1 and 7 have one digit, the listed two-digit
// zones two, every other code three.
func countryCodeLength(digits string) int {
	if digits == "" {
		return 0
	}
	switch digits[0] {
	case '1', '7':
		return 1
	}
	if len(digits) < 2 {
		return len(digits)
	}
	two := digits[:2]
	switch two {
	case "20", "27", "30", "31", "32", "33", "34", "36", "39", "40", "41", "43", "44", "45", "46", "47", "48", "49",
		"51", "52", "53", "54", "55", "56", "57", "58", "60", "61", "62", "63", "64", "65", "66",
		"81", "82", "84", "86", "90", "91", "92", "93", "94", "95", "98":
		return 2
	}
	return 3
}

// minMaskedDigits is the least number of digits MaskMobile always hides.
const minMaskedDigits = 2

// MaskMobile shows country code, three digits and the last two of a
// number: +4915112345689 → +49151******89. A short number (E.164 allows 7
// digits, +6831234) shows fewer leading digits, so at least
// minMaskedDigits stay hidden: +683**34. An empty number stays empty.
func MaskMobile(number string) string {
	digits := strings.TrimPrefix(number, "+")
	if digits == "" {
		return ""
	}
	tail := min(2, len(digits))
	head := countryCodeLength(digits) + 3
	if head+tail+minMaskedDigits > len(digits) {
		head = max(len(digits)-tail-minMaskedDigits, 0)
	}
	if head+tail > len(digits) {
		tail = len(digits) - head
	}
	return "+" + digits[:head] + strings.Repeat("*", len(digits)-head-tail) + digits[len(digits)-tail:]
}

// setMobileTx stores number as the user's mobile inside the tenant
// transaction: a new subject replaces the former one (deleted), an empty
// number deletes it. It returns the new surrogate ("" when cleared).
func setMobileTx(ctx context.Context, tx pgx.Tx, orgID, userID, number string) (string, error) {
	var current string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(mobile_pii_subject_id::text, '') FROM app_user
		WHERE id = $1 AND organization_id = $2 FOR UPDATE`, userID, orgID).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("user not found")
		}
		return "", fmt.Errorf("lock user for mobile: %w", err)
	}
	next := ""
	if number != "" {
		vault := pii.Default()
		if vault == nil {
			return "", ErrNoVault
		}
		var err error
		if next, err = vault.CreateTx(ctx, tx, orgID, pii.KindUser, pii.Data{mobileField: number}); err != nil {
			return "", fmt.Errorf("store mobile: %w", err)
		}
	}
	if next == "" && current == "" {
		return "", nil
	}
	if _, err := tx.Exec(ctx, `UPDATE app_user SET mobile_pii_subject_id = $3, updated_at = NOW()
		WHERE id = $1 AND organization_id = $2`, userID, orgID, nilIfEmpty(next)); err != nil {
		return "", fmt.Errorf("set mobile: %w", err)
	}
	if current != "" {
		if err := pii.DeleteTx(ctx, tx, orgID, current); err != nil && !errors.Is(err, pii.ErrNotFound) {
			return "", fmt.Errorf("delete former mobile: %w", err)
		}
	}
	return next, nil
}

// fillMobileTx resolves the mobile numbers of users in one vault read and
// sets MobileMasked (and Mobile when plaintext is true). Without a vault the
// numbers stay empty.
func fillMobileTx(ctx context.Context, tx pgx.Tx, orgID string, users []*User, plaintext bool) error {
	vault := pii.Default()
	if vault == nil {
		return nil
	}
	ids := make([]string, 0, len(users))
	for _, u := range users {
		if u.MobileSubjectID != "" {
			ids = append(ids, u.MobileSubjectID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	subjects, err := vault.ResolveTx(ctx, tx, orgID, ids)
	if err != nil {
		return fmt.Errorf("resolve mobile numbers: %w", err)
	}
	for _, u := range users {
		s := subjects[u.MobileSubjectID]
		if s == nil {
			continue
		}
		number := s.Data[mobileField]
		u.MobileMasked = MaskMobile(number)
		if plaintext {
			u.Mobile = number
		}
	}
	return nil
}
