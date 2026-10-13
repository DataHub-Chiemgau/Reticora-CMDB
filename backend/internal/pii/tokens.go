package pii

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// searchKeyLabel derives the key of the blind index from the data key.
const searchKeyLabel = "reticora/pii-search/v1"

// words are the searchable forms of a value: its words (letters and
// digits, at least two characters), the whole value, and the digits of a
// phone number.
func words(value string) []string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return nil
	}
	out := []string{value}
	for _, w := range strings.FieldsFunc(value, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) >= 2 {
			out = append(out, w)
		}
	}
	digits := strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) {
			return r
		}
		return -1
	}, value)
	if len(digits) >= 4 && digits != value {
		out = append(out, digits)
	}
	return out
}

// blindToken is the keyed hash of a word: equal words give equal tokens
// within the organization, the word itself is not stored.
func blindToken(key []byte, word string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(word))
	return hex.EncodeToString(mac.Sum(nil))[:24]
}

func searchKey(dek []byte) []byte {
	mac := hmac.New(sha256.New, dek)
	mac.Write([]byte(searchKeyLabel))
	return mac.Sum(nil)
}

// searchTokens is the blind index of a subject's data; value subjects of
// the history are not searchable.
func searchTokens(dek []byte, kind string, data Data) []string {
	if kind == KindValue {
		return []string{}
	}
	key := searchKey(dek)
	seen := map[string]bool{}
	out := []string{}
	for _, v := range data {
		for _, w := range words(v) {
			if t := blindToken(key, w); !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	sort.Strings(out)
	return out
}

// QueryTokensTx returns the blind tokens of the words of a search text
// under the organization's current data key: a subject matches the text
// when its search_tokens contain all of them (search_tokens @> tokens).
// An empty text has no tokens.
func (v *Vault) QueryTokensTx(ctx context.Context, tx pgx.Tx, orgID, text string) ([]string, error) {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return nil, nil
	}
	dek, _, err := v.orgKey(ctx, tx, orgID)
	if err != nil {
		return nil, err
	}
	key := searchKey(dek)
	var out []string
	for _, w := range strings.Fields(text) {
		out = append(out, blindToken(key, strings.Trim(w, `"'.,;:()`)))
	}
	return out, nil
}

// ExactTokenTx is the blind token of a whole value (an e-mail address).
func (v *Vault) ExactTokenTx(ctx context.Context, tx pgx.Tx, orgID, value string) (string, error) {
	dek, _, err := v.orgKey(ctx, tx, orgID)
	if err != nil {
		return "", err
	}
	return blindToken(searchKey(dek), strings.ToLower(strings.TrimSpace(value))), nil
}
