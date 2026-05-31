package kubernetes

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestObfuscateForLog_SecretDefaults locks in the default behaviour
// that drives #308: a Secret v1 manifest with no explicit
// sensitive_fields still has its data / stringData values replaced by
// the placeholder, so the [DEBUG] log path cannot leak the values.
func TestObfuscateForLog_SecretDefaults(t *testing.T) {
	const yamlBody = `apiVersion: v1
kind: Secret
metadata:
  name: leak-demo
  namespace: default
stringData:
  password: supersecret
data:
  api_token: dG9wc2VjcmV0
`
	out := obfuscateForLog(yamlBody, "", nil)

	require.Contains(t, out, "(sensitive value)", "obfuscation placeholder must appear")
	assert.NotContains(t, out, "supersecret",
		"raw stringData value must not appear in the log payload")
	assert.NotContains(t, out, "dG9wc2VjcmV0",
		"raw data value must not appear in the log payload")
	// metadata.name is not sensitive and should round-trip so the
	// operator can still identify which manifest the log refers to.
	assert.Contains(t, out, "leak-demo")
}

// TestObfuscateForLog_CustomFields exercises the explicit
// sensitive_fields path (any Kind, any apiVersion).
func TestObfuscateForLog_CustomFields(t *testing.T) {
	const yamlBody = `apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
data:
  api_key: hunter2
  log_level: info
`
	out := obfuscateForLog(yamlBody, "", []string{"data.api_key"})

	assert.NotContains(t, out, "hunter2",
		"sensitive_fields path must replace the value")
	assert.Contains(t, out, "log_level: info",
		"non-sensitive sibling values must round-trip")
}

// TestObfuscateForLog_BlankEntriesDontSuppressSecretDefault covers the
// CodeRabbit follow-up to #308: a misconfigured sensitive_fields list
// like [""] previously made `len(fields) != 0` in BuildObfuscatedYAML,
// silently skipping the Secret v1 default ("data" + "stringData") and
// leaking the very payload the masking exists to hide. With the
// NormalizeSensitiveFields fix, blank-only input collapses to nil and
// the default re-activates.
func TestObfuscateForLog_BlankEntriesDontSuppressSecretDefault(t *testing.T) {
	const yamlBody = `apiVersion: v1
kind: Secret
metadata:
  name: leak-demo
  namespace: default
stringData:
  password: supersecret
`
	for name, in := range map[string][]string{
		"single empty":     {""},
		"whitespace only":  {"   ", "\t"},
		"mixed empty real": {"", "   "},
	} {
		t.Run(name, func(t *testing.T) {
			out := obfuscateForLog(yamlBody, "", in)
			assert.NotContains(t, out, "supersecret",
				"blank-only sensitive_fields must still trigger Secret v1 default masking, got: %q", out)
			assert.Contains(t, out, "(sensitive value)")
		})
	}
}

// TestNormalizeSensitiveFields exercises the exported normalizer
// directly: empties / whitespace / nil collapse to nil; real entries
// pass through unchanged.
func TestNormalizeSensitiveFields(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, nil},
		{"empty slice", []string{}, nil},
		{"single empty", []string{""}, nil},
		{"whitespace only", []string{"  ", "\t\n"}, nil},
		{"real entry", []string{"data.password"}, []string{"data.password"}},
		{"real + blank", []string{"data.password", ""}, []string{"data.password"}},
		{"blank + real", []string{"", "data.token"}, []string{"data.token"}},
		{"two reals", []string{"a", "b"}, []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, NormalizeSensitiveFields(tc.in))
		})
	}
}

// TestObfuscateForLog_FailsClosed covers the security-critical fallback:
// a malformed sensitive_fields path makes BuildObfuscatedYAML return an
// error, and the helper must NOT emit the raw body in that case.
func TestObfuscateForLog_FailsClosed(t *testing.T) {
	const yamlBody = `apiVersion: v1
kind: Secret
metadata:
  name: leak-demo
stringData:
  password: supersecret
`
	// Path that resolves to a scalar rather than a map; BuildObfuscatedYAML
	// rejects this with "only map values are supported".
	out := obfuscateForLog(yamlBody, "", []string{"stringData.password.deeper"})

	assert.NotContains(t, out, "supersecret",
		"fail-closed: raw body must be suppressed when obfuscation errors")
	assert.True(t,
		strings.Contains(out, "obfuscation failed") &&
			strings.Contains(out, "body suppressed"),
		"placeholder must mention both 'obfuscation failed' and 'body suppressed', got: %q",
		out)
}
