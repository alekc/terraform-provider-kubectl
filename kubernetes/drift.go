package kubernetes

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"reflect"
	"sort"
	"strings"

	yamlWriter "sigs.k8s.io/yaml"
)

// drift.go renders a human-readable diff between a desired manifest and the
// live manifest as a YAML subtree containing only the paths that differ.
//
// This is the visibility layer that replaces opaque sha256 fingerprints in
// yaml_incluster / live_manifest_incluster. The fingerprint approach made
// drift detection work but hid which fields actually changed; the YAML
// output mirrors `kubectl diff` semantics so the plan diff itself tells
// the user what's drifted without TRACE-level logging.
//
// The function is pure: no API calls, no global state. Callers pass two
// unstructured maps and get back a YAML string (empty when in sync).

// ShowMode controls how drifted leaf values are rendered. The default is
// ShowNone: paths are visible but values are not.
type ShowMode string

const (
	// ShowNone renders each drifted leaf as the literal marker `<drift>`.
	// Safe default: paths only, no values.
	ShowNone ShowMode = "none"

	// ShowHash renders each drifted leaf as `<was:HASH now:HASH>` with
	// 8-char sha256 prefixes. Confirms a value changed without showing
	// it. Useful for sensitive workloads where the value itself is
	// confidential but the fact of the change is not.
	ShowHash ShowMode = "hash"

	// ShowFull renders each drifted leaf as `<was: V1, now: V2>` with
	// the actual before / after values. Parity with `kubectl diff`.
	// Secret kinds, ignore_fields paths, and mask_paths globs still
	// mask their leaves regardless of mode.
	ShowFull ShowMode = "full"
)

// driftMarker is the literal string used for drifted leaves under ShowNone
// and for any leaf that gets force-masked (Secret data, mask_paths). It is
// not a valid Kubernetes value, so accidentally round-tripping the drift
// YAML through `kubectl apply` would surface a clear error rather than a
// silent corruption.
const driftMarker = "<drift>"

// sensitiveDriftMarker is used at masked leaves to make it clear to the
// reader that the value is hidden by policy, not by mode. The path is
// still visible.
const sensitiveDriftMarker = "<drift sensitive>"

// missingMarker is rendered at array indices that exist in desired but
// not in live (or vice versa).
const missingMarker = "<missing>"

// DriftOptions configures the rendering. Zero value is valid:
// ShowMode defaults to ShowNone; the empty Kind/APIVersion skips Secret
// auto-masking; nil slices add no extra exclusions / masks.
type DriftOptions struct {
	// IgnoreFields lists dot-paths that are excluded from the comparison
	// entirely. Matches the legacy ignore_fields semantics: an entry
	// "spec.x" excludes spec.x and every descendant.
	IgnoreFields []string

	// MaskPaths lists glob-paths whose leaves render as a sensitive
	// marker regardless of ShowMode. Globs support `*` (one segment)
	// and `**` (zero or more segments). The path "spec.template.spec"
	// or the glob "**.password" both work.
	MaskPaths []string

	// ShowMode controls the leaf rendering for drifted, non-masked
	// values. Defaults to ShowNone.
	ShowMode ShowMode

	// Kind and APIVersion enable kind-aware auto-masking. When Kind ==
	// "Secret" and APIVersion == "v1", the paths `data.*` and
	// `stringData.*` are masked automatically.
	Kind       string
	APIVersion string
}

// RenderDrift returns a YAML string containing only the paths under which
// desired and live differ, plus enough parent scaffolding to locate them.
// Returns the empty string when the two manifests agree on every relevant
// path. Behaviour is deterministic: map keys are sorted; array drift is
// reported by index.
//
// "Agree" means: trim-string-equal at the leaves, recursive descent at
// maps, index-aligned at slices. Fields present in live but absent from
// desired are ignored (they are usually server-side defaulting and
// controller injection). Fields present in desired but absent from live
// are reported as <missing>.
func RenderDrift(desired, live map[string]interface{}, opts DriftOptions) string {
	if desired == nil {
		desired = map[string]interface{}{}
	}
	if live == nil {
		live = map[string]interface{}{}
	}

	ignore := buildIgnoreSet(opts.IgnoreFields)
	masks := buildMaskGlobs(opts.MaskPaths, opts.Kind, opts.APIVersion)

	tree, hasDrift := collectMapDrift(desired, live, nil, ignore, masks, opts.ShowMode)
	if !hasDrift {
		return ""
	}
	out, err := yamlWriter.Marshal(tree)
	if err != nil {
		// yamlWriter.Marshal on a tree of map[string]interface{} and
		// []interface{} with primitive leaves cannot fail in practice;
		// returning a sentinel rather than panicking keeps the
		// function pure.
		return fmt.Sprintf("# drift rendering failed: %v\n", err)
	}
	return string(out)
}

// collectMapDrift walks a map pair and returns the drift subtree plus a
// bool indicating whether anything drifted under this node.
func collectMapDrift(desired, live map[string]interface{}, path []string, ignore ignoreSet, masks []maskGlob, mode ShowMode) (map[string]interface{}, bool) {
	result := map[string]interface{}{}
	keys := make([]string, 0, len(desired))
	for k := range desired {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	any := false
	for _, k := range keys {
		childPath := append(path, k) //nolint:gocritic // intentional non-aliasing per recursion
		if ignore.matches(childPath) {
			continue
		}
		dv := desired[k]
		var lv interface{}
		var hasInLive bool
		if live != nil {
			lv, hasInLive = live[k]
		}
		if !hasInLive {
			// Desired has it, live doesn't. This case is common and
			// rarely meaningful: the apiserver strips fields the
			// user provided but the kind doesn't accept
			// (metadata.namespace on cluster-scoped resources is
			// the classic offender, injected by override_namespace),
			// or the user just hasn't applied yet. The legacy v2
			// fingerprint treated this asymmetrically too: a
			// missing-in-live key did not contribute to the
			// fingerprint, so plans were no-ops in this case.
			// Reporting it as drift here triggers infinite update
			// loops (each refresh sees drift, plan wants an update,
			// apply doesn't change live, next refresh sees drift
			// again). Server-side drift_engine sidesteps this
			// because the dry-run apply response also lacks the
			// stripped field, so the comparison agrees with live.
			// Keep the TRACE log so the case is still observable
			// for users grepping debug output.
			log.Printf("[TRACE] desired-only path (live lacks key): %s", strings.Join(childPath, "."))
			continue
		}
		child, drifted := collectAnyDrift(dv, lv, childPath, ignore, masks, mode)
		if drifted {
			result[k] = child
			any = true
		}
	}
	return result, any
}

// collectAnyDrift dispatches on the desired value's concrete type.
func collectAnyDrift(desired, live interface{}, path []string, ignore ignoreSet, masks []maskGlob, mode ShowMode) (interface{}, bool) {
	switch d := desired.(type) {
	case map[string]interface{}:
		l, _ := live.(map[string]interface{})
		// If live is not a map, type mismatch == drift at this node.
		if live != nil && l == nil {
			return renderLeaf(d, live, path, masks, mode), true
		}
		return collectMapDrift(d, l, path, ignore, masks, mode)
	case []interface{}:
		l, ok := live.([]interface{})
		if !ok {
			if live == nil {
				return renderLeaf(d, nil, path, masks, mode), true
			}
			return renderLeaf(d, live, path, masks, mode), true
		}
		return collectSliceDrift(d, l, path, ignore, masks, mode)
	default:
		if leafEqual(desired, live) {
			return nil, false
		}
		return renderLeaf(desired, live, path, masks, mode), true
	}
}

// collectSliceDrift compares two []interface{} by index. Length mismatches
// are reported with explicit <missing> entries for the trailing positions
// on whichever side is shorter.
func collectSliceDrift(desired, live []interface{}, path []string, ignore ignoreSet, masks []maskGlob, mode ShowMode) ([]interface{}, bool) {
	var out []interface{}
	any := false
	max := len(desired)
	if len(live) > max {
		max = len(live)
	}
	for i := 0; i < max; i++ {
		var d, l interface{}
		hasD := i < len(desired)
		hasL := i < len(live)
		if hasD {
			d = desired[i]
		}
		if hasL {
			l = live[i]
		}
		idxPath := append(path, fmt.Sprintf("[%d]", i)) //nolint:gocritic // intentional non-aliasing
		switch {
		case hasD && hasL:
			child, drifted := collectAnyDrift(d, l, idxPath, ignore, masks, mode)
			if drifted {
				out = append(out, wrapIndexedItem(i, child))
				any = true
			}
		case hasD && !hasL:
			// desired has more items than live. Treated as
			// non-drift for the same reason as the map-key case
			// in collectMapDrift: the apiserver may have trimmed
			// the list (strategic merge, list-type semantics),
			// and surfacing it here triggers infinite update
			// loops on resources where apply produces a shorter
			// list than the user wrote. TRACE-log instead.
			log.Printf("[TRACE] desired-only index (live shorter): %s", strings.Join(idxPath, "."))
		case !hasD && hasL:
			// live has more items than desired; typically server-injected
			// defaulting (e.g. status). Skip.
		}
	}
	return out, any
}

// wrapIndexedItem decorates a drift subtree for an array element with its
// index so the reader can locate the position even when only one of many
// elements drifted.
func wrapIndexedItem(idx int, child interface{}) interface{} {
	switch v := child.(type) {
	case map[string]interface{}:
		// Avoid clobbering a user key literally named "_index_".
		if _, exists := v["_index_"]; !exists {
			v["_index_"] = idx
			return v
		}
		return map[string]interface{}{
			"_index_": idx,
			"_value_": v,
		}
	default:
		return map[string]interface{}{
			"_index_": idx,
			"_value_": v,
		}
	}
}

// renderLeaf produces the leaf representation for a drifted scalar / type
// mismatch. masks and mode together control how visible the values are.
func renderLeaf(desired, live interface{}, path []string, masks []maskGlob, mode ShowMode) interface{} {
	if pathMasked(path, masks) {
		return sensitiveDriftMarker
	}
	switch mode {
	case ShowFull:
		return fmt.Sprintf("<was: %s, now: %s>", formatValue(desired), formatValue(live))
	case ShowHash:
		return fmt.Sprintf("<was:%s now:%s>", shortHash(desired), shortHash(live))
	default:
		return driftMarker
	}
}

// formatValue renders a value for the ShowFull mode. Strings get quoted;
// other scalars use their natural Go form; complex values (a type mismatch
// between desired and live, e.g. desired is a map and live is a string)
// stringify via fmt.
func formatValue(v interface{}) string {
	if v == nil {
		return missingMarker
	}
	switch s := v.(type) {
	case string:
		return fmt.Sprintf("%q", s)
	case bool, int, int32, int64, uint, uint32, uint64, float32, float64:
		return fmt.Sprintf("%v", s)
	default:
		// Map / slice rendering for type-mismatch cases. Inline as a
		// short YAML; if marshalling fails just fall back to %v.
		buf, err := yamlWriter.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return strings.TrimRight(string(buf), "\n")
	}
}

// shortHash returns the first 8 hex chars of the sha256 of fmt.Sprintf %v
// of the value. Stable across runs (no randomness, no timestamps), but
// distinguishes "different value" from "same value" without revealing it.
func shortHash(v interface{}) string {
	if v == nil {
		return "missing"
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%v", v)))
	return hex.EncodeToString(h[:])[:8]
}

// leafEqual is the equality check for scalars. Strings get trimmed first
// because the legacy code did the same and a lot of yaml-vs-apiserver
// round-tripping introduces gratuitous whitespace differences. Other
// types use reflect.DeepEqual; numeric types are not coerced (we treat
// int and float64 as distinct even when numerically equal, because the
// apiserver only emits one form per field).
func leafEqual(desired, live interface{}) bool {
	if desired == nil && live == nil {
		return true
	}
	if desired == nil || live == nil {
		return false
	}
	if ds, ok := desired.(string); ok {
		if ls, ok2 := live.(string); ok2 {
			return strings.TrimSpace(ds) == strings.TrimSpace(ls)
		}
	}
	return reflect.DeepEqual(desired, live)
}

// ignoreSet packages the dotted paths that should be skipped entirely.
// A path entry "spec.x" matches the exact path and every descendant.
type ignoreSet struct {
	prefixes []string
}

func buildIgnoreSet(extra []string) ignoreSet {
	all := append([]string{}, kubernetesControlFields...)
	for _, e := range extra {
		e = strings.TrimSpace(e)
		if e != "" {
			all = append(all, e)
		}
	}
	return ignoreSet{prefixes: all}
}

func (s ignoreSet) matches(path []string) bool {
	if len(path) == 0 {
		return false
	}
	joined := strings.Join(path, ".")
	for _, p := range s.prefixes {
		if joined == p || strings.HasPrefix(joined, p+".") {
			return true
		}
	}
	return false
}

// maskGlob describes one entry in the mask list. Supports literal segments,
// "*" (one segment), and "**" (zero or more segments).
type maskGlob struct {
	segments []string
}

func buildMaskGlobs(extra []string, kind, apiVersion string) []maskGlob {
	var out []maskGlob
	for _, e := range extra {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		base := splitPath(e)
		// Literal user entries match the path itself AND every
		// descendant: a user writing `mask_paths = ["data"]`
		// expects `data.password` to be masked too, not just a
		// scalar at `data`. Entries that already end in a `**`
		// glob are left alone. Entries that contain glob
		// metacharacters (`*` or `**`) are also taken literally;
		// users who want a leaf-only pattern can still write
		// `mask_paths = ["data.*"]` to get exactly-one-segment
		// children.
		out = append(out, maskGlob{segments: base})
		if !endsWithDoubleStar(base) {
			descendants := make([]string, 0, len(base)+1)
			descendants = append(descendants, base...)
			descendants = append(descendants, "**")
			out = append(out, maskGlob{segments: descendants})
		}
	}
	if kind == "Secret" && apiVersion == "v1" {
		out = append(out,
			maskGlob{segments: []string{"data", "**"}},
			maskGlob{segments: []string{"data"}},
			maskGlob{segments: []string{"stringData", "**"}},
			maskGlob{segments: []string{"stringData"}},
		)
	}
	return out
}

// endsWithDoubleStar reports whether the last segment of the pattern
// is the multi-segment glob `**`. Used to skip the auto-expanded
// subtree mask in buildMaskGlobs when the user already wrote one.
func endsWithDoubleStar(segments []string) bool {
	if len(segments) == 0 {
		return false
	}
	return segments[len(segments)-1] == "**"
}

// splitPath turns a dotted glob into segments. The only metacharacters
// are `*` and `**`; everything else is a literal.
func splitPath(p string) []string {
	return strings.Split(p, ".")
}

func pathMasked(path []string, masks []maskGlob) bool {
	for _, g := range masks {
		if globMatch(g.segments, path) {
			return true
		}
	}
	return false
}

// globMatch implements pattern matching for the two metacharacters `*`
// (matches exactly one path segment) and `**` (matches zero or more
// segments). Array indices in paths arrive as "[N]" which is treated as
// a single segment by the matcher.
func globMatch(pattern, path []string) bool {
	pi, ti := 0, 0
	starStar := -1
	starStarTi := 0
	for ti < len(path) {
		if pi < len(pattern) {
			switch pattern[pi] {
			case "**":
				starStar = pi
				starStarTi = ti
				pi++
				continue
			case "*":
				pi++
				ti++
				continue
			default:
				if pattern[pi] == path[ti] {
					pi++
					ti++
					continue
				}
			}
		}
		if starStar >= 0 {
			pi = starStar + 1
			starStarTi++
			ti = starStarTi
			continue
		}
		return false
	}
	for pi < len(pattern) && pattern[pi] == "**" {
		pi++
	}
	return pi == len(pattern)
}
