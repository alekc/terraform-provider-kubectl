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
