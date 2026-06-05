package kubernetes

import (
	"fmt"
	"strconv"
	"strings"
)

// path_walker.go: dot-and-bracket path traversal for `fields`,
// `wait_for.field`, and the data-source / ephemeral-resource
// extraction helpers. Replaces gojsonq, which split on `.`
// unconditionally and could not address keys whose own name
// contained dots: Kubernetes labels and annotations with domain
// prefixes (`app.kubernetes.io/name`,
// `argocd.argoproj.io/sync-wave`,
// `nginx.ingress.kubernetes.io/rewrite-target`) were therefore
// unreachable via the documented dot syntax. Issue #271.
//
// The new syntax is a backward-compatible superset:
//
//   - `metadata.name` -> map key "metadata", then "name". As before.
//   - `containers.0` or `containers.[0]` -> array index 0. As before.
//   - `metadata.labels["app.kubernetes.io/name"]` -> map key with
//     embedded dots. New. Either double quotes or single quotes are
//     accepted; the quote character chosen does not need to be
//     escaped inside the segment because the bracket-balancing
//     parser already determines the segment boundary.
//   - `spec.containers[0].image` -> bracketed index without a
//     leading dot. New, matches how most JSONPath dialects let
//     users write array access directly after a key. Equivalent to
//     `spec.containers.[0].image` and `spec.containers.0.image`.
//
// The walker returns (value, found, err): found is false when a
// segment is absent on its parent, true when the segment exists
// even if the value is explicitly null. Callers that previously
// disambiguated nil-not-found from nil-explicit-null via a separate
// existence check (jsonPathExists) can now consume found directly.

// ExtractByPath walks doc following the dot-and-bracket path and
// returns the value at that path. found is false when the path
// terminates at a missing key or out-of-range index; err is
// non-nil only for malformed paths (unbalanced brackets, empty
// segments) or type mismatches between path segment and value
// shape (e.g. a non-integer segment against a slice).
func ExtractByPath(doc interface{}, path string) (interface{}, bool, error) {
	segments, err := parsePathSegments(path)
	if err != nil {
		return nil, false, err
	}
	current := doc
	for _, seg := range segments {
		switch node := current.(type) {
		case map[string]interface{}:
			v, ok := node[seg.key]
			if !ok {
				return nil, false, nil
			}
			current = v
		case []interface{}:
			idx, err := segmentToIndex(seg)
			if err != nil {
				return nil, false, err
			}
			if idx < 0 || idx >= len(node) {
				return nil, false, nil
			}
			current = node[idx]
		case nil:
			// Walking past an explicit null is "not found".
			return nil, false, nil
		default:
			// Scalar mid-walk: the user's path goes deeper than
			// the document; treat as not-found rather than
			// raising an error, matching the previous gojsonq
			// behaviour where Find returned nil at this point.
			return nil, false, nil
		}
	}
	return current, true, nil
}

// pathSegment captures one step of a parsed path. quoted=true
// indicates the segment came from a bracketed string form
// (`["foo.bar"]`) so it must be used as a map key verbatim and not
// reinterpreted as an array index. bracketedIndex=true indicates a
// bare integer bracketed form (`[0]`) so it must be used as a slice
// index and not as a literal map key.
type pathSegment struct {
	key            string
	quoted         bool
	bracketedIndex bool
}

// parsePathSegments lexes path into a slice of segments. The grammar:
//
//	path     = segment ( ('.' segment) | bracket )*
//	segment  = identifier | bracket
//	bracket  = '[' ( quoted | integer | identifier ) ']'
//	quoted   = '"' chars '"' | "'" chars "'"
//	integer  = digits
//	identifier = chars excluding '.' '[' ']'
//
// Empty paths and empty segments are rejected as malformed. Quote
// characters inside a quoted bracket segment do not need escaping;
// the parser closes on the first matching close-bracket after the
// closing quote.
func parsePathSegments(path string) ([]pathSegment, error) {
	if path == "" {
		return nil, fmt.Errorf("empty path")
	}
	var out []pathSegment
	i := 0
	for i < len(path) {
		c := path[i]
		switch {
		case c == '.':
			// A leading or doubled dot would imply an empty
			// segment. Reject so misconfigured paths surface
			// loudly rather than as silent not-found.
			if i == 0 || path[i-1] == '.' || path[i-1] == ']' && i == 0 {
				return nil, fmt.Errorf("empty segment in path %q at position %d", path, i)
			}
			i++
		case c == '[':
			seg, next, err := parseBracketSegment(path, i)
			if err != nil {
				return nil, err
			}
			out = append(out, seg)
			i = next
		case c == ']':
			return nil, fmt.Errorf("unexpected ']' in path %q at position %d", path, i)
		default:
			// Read a bare identifier up to next '.' or '['.
			start := i
			for i < len(path) && path[i] != '.' && path[i] != '[' {
				if path[i] == ']' {
					return nil, fmt.Errorf("unexpected ']' in path %q at position %d", path, i)
				}
				i++
			}
			ident := path[start:i]
			if ident == "" {
				return nil, fmt.Errorf("empty identifier in path %q at position %d", path, start)
			}
			out = append(out, pathSegment{key: ident})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("path %q produced no segments", path)
	}
	return out, nil
}

// parseBracketSegment consumes a `[...]` token starting at position
// i and returns the resulting segment plus the index of the
// character immediately after the closing `]`.
func parseBracketSegment(path string, i int) (pathSegment, int, error) {
	// i points at '['
	if i+1 >= len(path) {
		return pathSegment{}, 0, fmt.Errorf("unterminated '[' in path %q at position %d", path, i)
	}
	j := i + 1
	first := path[j]
	if first == '"' || first == '\'' {
		// Quoted form. Read until the matching quote, then
		// expect the very next char to be ']'.
		quote := first
		j++
		start := j
		for j < len(path) && path[j] != quote {
			j++
		}
		if j >= len(path) {
			return pathSegment{}, 0, fmt.Errorf("unterminated quoted bracket segment in path %q at position %d", path, i)
		}
		key := path[start:j]
		j++ // consume close quote
		if j >= len(path) || path[j] != ']' {
			return pathSegment{}, 0, fmt.Errorf("expected ']' after quoted segment in path %q at position %d", path, j)
		}
		return pathSegment{key: key, quoted: true}, j + 1, nil
	}
	// Unquoted form: read until ']'. The contents may be either a
	// pure integer (slice index) or a bare identifier (map key).
	// Bare identifiers cannot contain dots; users who need that
	// must reach for the quoted form. We treat purely-numeric
	// content as an index and any other content as a key, which
	// preserves the legacy `[N]` syntax.
	start := j
	for j < len(path) && path[j] != ']' {
		j++
	}
	if j >= len(path) {
		return pathSegment{}, 0, fmt.Errorf("unterminated bracket segment in path %q at position %d", path, i)
	}
	body := path[start:j]
	if body == "" {
		return pathSegment{}, 0, fmt.Errorf("empty bracket segment in path %q at position %d", path, i)
	}
	if _, err := strconv.Atoi(body); err == nil {
		return pathSegment{key: body, bracketedIndex: true}, j + 1, nil
	}
	return pathSegment{key: body}, j + 1, nil
}

// segmentToIndex returns the integer index implied by a segment
// when the current node is a slice. Pure-integer literal segments
// (`containers.0`) and bracketed-integer segments (`containers[0]`)
// both yield a valid index; quoted segments are explicit map-key
// requests and must not be coerced into an index.
func segmentToIndex(seg pathSegment) (int, error) {
	if seg.quoted {
		return 0, fmt.Errorf("quoted segment %q is not a valid array index", seg.key)
	}
	idx, err := strconv.Atoi(seg.key)
	if err != nil {
		return 0, fmt.Errorf("segment %q is not a valid array index", seg.key)
	}
	return idx, nil
}

// pathContainsBracket is a cheap heuristic used by callers that
// want to log whether a user has reached for the new syntax. Not
// part of the parsing flow.
func pathContainsBracket(path string) bool {
	return strings.ContainsAny(path, "[]")
}
