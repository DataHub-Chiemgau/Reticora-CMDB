package ci

import "reflect"

// MergePatch applies an RFC 7396 JSON merge patch to target and returns the
// result; target is left unchanged. A null in the patch removes the member,
// an object is merged recursively (replacing a non-object target value), and
// every other value replaces the member (CI-04).
func MergePatch(target, patch map[string]any) map[string]any {
	out := make(map[string]any, len(target)+len(patch))
	for k, v := range target {
		out[k] = v
	}
	for k, pv := range patch {
		switch p := pv.(type) {
		case nil:
			delete(out, k)
		case map[string]any:
			current, _ := out[k].(map[string]any)
			out[k] = MergePatch(current, p)
		default:
			out[k] = pv
		}
	}
	return out
}

// changedKeys returns the top-level members whose value differs between
// before and after, including added and removed members.
func changedKeys(before, after map[string]any) []string {
	var keys []string
	for k, v := range after {
		if old, ok := before[k]; !ok || !reflect.DeepEqual(old, v) {
			keys = append(keys, k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			keys = append(keys, k)
		}
	}
	return keys
}
