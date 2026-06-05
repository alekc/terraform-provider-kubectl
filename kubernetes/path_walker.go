package kubernetes

import (
	"fmt"
	"strconv"
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
			if seg.bracketedIndex {
				// `[N]` was used at a map node. Almost always
				// a user typo (e.g. `metadata.annotations[0]`
				// when the user thought annotations was a
				// list). Surface loudly rather than silently
				// look up the literal string key "N".
				return nil, false, fmt.Errorf("segment [%s] applied to a map (expected an array)", seg.key)
			}
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
		default:
			// Scalar mid-walk or walking past an explicit null:
			// the path goes deeper than the document. Treat as
			// not-found rather than raising an error, matching
			// the previous gojsonq behaviour where Find returned
			// nil at this point. extractFields then surfaces the
			// original "path not found in fetched object" diagnostic
			// at the caller.
			return nil, false, nil
		}
	}
	return current, true, nil
}

// pathSegment captures one step of a parsed path.
//
//   - quoted=true: segment came from a quoted bracketed form
//     (`["foo.bar"]`). It must be used as a map key verbatim and
//     can never be reinterpreted as an array index, so applying it
//     to a slice surfaces an error.
//   - bracketedIndex=true: segment came from an unquoted-numeric
//     bracketed form (`[0]`). It must be used as a slice index;
//     applying it to a map surfaces an error rather than silently
//     looking up the literal string key (which would mask user
//     typos like `metadata.annotations[0]` written when an array
//     was expected).
//   - Both false: segment came from a bare dotted identifier
//     (`metadata.name`, `containers.0`). The dispatch rules in
//     ExtractByPath decide map-vs-slice handling from the parent
//     node's runtime type.
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
// Empty paths, leading dots, doubled dots, trailing dots, and empty
// bracket segments are all rejected as malformed. Quote characters
// inside a quoted bracket segment do not need escaping; the parser
// closes on the first matching close-bracket after the closing
// quote (so the chosen quote character cannot itself appear in the
// segment, which is fine for valid Kubernetes label keys).
func parsePathSegments(path string) ([]pathSegment, error) {
	if path == "" {
		return nil, fmt.Errorf("empty path")
	}
	if path[len(path)-1] == '.' {
		return nil, fmt.Errorf("empty segment at end of path %q", path)
	}
	var out []pathSegment
	i := 0
	for i < len(path) {
		c := path[i]
		switch {
		case c == '.':
			// A leading or doubled dot implies an empty
			// segment. Reject so misconfigured paths surface
			// loudly rather than as silent not-found.
			if i == 0 || path[i-1] == '.' {
				return nil, fmt.Errorf("empty segment in path %q at position %d", path, i)
			}
			i++
		case c == '[':
			seg, next, err := parseBracketSegment(path, i)
			if err != nil {
				return nil, err
			}
			// After a `]`, the only legal continuations are
			// end-of-path, a `.` introducing the next bare
			// segment, or another `[` introducing another
			// bracket. Anything else (e.g. `x[0]y`) is a syntax
			// error: it would otherwise silently parse as
			// `x[0].y` and mask the missing separator.
			if next < len(path) && path[next] != '.' && path[next] != '[' {
				return nil, fmt.Errorf("expected '.' or '[' after ']' in path %q at position %d", path, next)
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
		if key == "" {
			return pathSegment{}, 0, fmt.Errorf("empty quoted bracket segment in path %q at position %d", path, i)
		}
		j++ // consume close quote
		if j >= len(path) || path[j] != ']' {
			return pathSegment{}, 0, fmt.Errorf("expected ']' after quoted segment in path %q at position %d", path, j)
		}
		return pathSegment{key: key, quoted: true}, j + 1, nil
	}
	// Unquoted form: read until ']'. The contents must be a pure
	// integer (slice index) or a bare identifier (map key); bare
	// identifiers cannot contain dots, slashes, or quotes, so users
	// who need those must reach for the quoted form.
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
	// Anything that LOOKS numeric (starts with a digit or a sign)
	// must match the strict unsigned-integer form. Signed forms
	// `[+1]` / `[-1]` are not valid: negative indices have no
	// meaning against `[]interface{}`, and a `+`-prefixed index is
	// a foot-gun for anyone expecting a signed offset. strconv.Atoi
	// would silently accept them. Mixed forms like `[1foo]` are
	// also rejected. Users who genuinely want a literal map key
	// that starts with a digit or sign must reach for the quoted
	// form `["1foo"]` / `["+1"]`.
	if body[0] == '+' || body[0] == '-' || (body[0] >= '0' && body[0] <= '9') {
		if !isASCIIDigits(body) {
			return pathSegment{}, 0, fmt.Errorf("invalid numeric bracket segment %q in path %q at position %d (only unsigned digits allowed; use quoted form for literal keys)", body, path, i)
		}
		// Pure unsigned-integer body. Tag the segment so
		// ExtractByPath can reject `[N]` against a map as a type
		// mismatch rather than silently looking up the literal
		// string key "N".
		return pathSegment{key: body, bracketedIndex: true}, j + 1, nil
	}
	return pathSegment{key: body}, j + 1, nil
}

// isASCIIDigits reports whether s is non-empty and consists only of
// the bytes '0'-'9'. Used as a strict pre-check before tagging a
// bracket body as a numeric index.
func isASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
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

