package discovery

import (
	"bytes"
	"encoding/json"
	"testing"
)

// sameScopeJSON is the former sameScope: two scopes are the same when
// their JSON encodings are equal. It is the oracle sameScope must match.
func sameScopeJSON(a, b *MessageScope) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

// TestSameScopeMatchesJSONEquality compares sameScope with the JSON
// comparison over every combination of nil, null, empty, ordered and
// differing id lists and user ids.
func TestSameScopeMatchesJSONEquality(t *testing.T) {
	lists := []func() *[]string{
		func() *[]string { return nil },
		func() *[]string { var ids []string; return &ids },
		func() *[]string { ids := []string{}; return &ids },
		func() *[]string { ids := []string{"a"}; return &ids },
		func() *[]string { ids := []string{"a", "b"}; return &ids },
		func() *[]string { ids := []string{"b", "a"}; return &ids },
		func() *[]string { ids := []string{"a", "b", "c"}; return &ids },
		func() *[]string { ids := []string{"<&>\"\u2028é"}; return &ids },
	}
	scopes := []*MessageScope{nil}
	for _, user := range []string{"", "u1"} {
		for _, c := range lists {
			for _, s := range lists {
				for _, tm := range lists {
					scopes = append(scopes, &MessageScope{UserID: user, Clients: c(), Sites: s(), Teams: tm()})
				}
			}
		}
	}
	// An empty scope and a second user id.
	scopes = append(scopes, &MessageScope{UserID: "u2"}, &MessageScope{})
	// The oracle on a sample of pairs; the full cross product compares the
	// encodings it would compute, each computed once.
	for i := 0; i < len(scopes); i += 7 {
		for j := 0; j < len(scopes); j += 5 {
			if got, want := sameScope(scopes[i], scopes[j]), sameScopeJSON(scopes[i], scopes[j]); got != want {
				t.Fatalf("sameScope(%+v, %+v) = %v, want %v", scopes[i], scopes[j], got, want)
			}
		}
	}
	encoded := make([][]byte, len(scopes))
	for i, s := range scopes {
		encoded[i], _ = json.Marshal(s)
	}
	equal := 0
	for i := range scopes {
		for j := range scopes {
			want := bytes.Equal(encoded[i], encoded[j])
			if want {
				equal++
			}
			if got := sameScope(scopes[i], scopes[j]); got != want {
				t.Fatalf("sameScope(%s, %s) = %v, want %v", encoded[i], encoded[j], got, want)
			}
		}
	}
	// nil/null pointers make some distinct scopes equal; the cross product
	// must cover such pairs, not only the diagonal.
	if equal <= len(scopes) {
		t.Fatalf("only %d equal pairs among %d scopes", equal, len(scopes))
	}
}
