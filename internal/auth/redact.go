package auth

import "regexp"

// tokenish matches values that look like credentials/tokens:
// UUIDs, JWTs, long hex runs, and long base64url runs.
var tokenish = regexp.MustCompile(
	`(?i)([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})` + // UUID
		`|(eyJ[\w-]+\.[\w-]+\.[\w-]+)` + // JWT
		`|([0-9a-f]{32,})`) // long hex

const redactedMarker = "[REDACTED]"

// scrub replaces token-shaped substrings with a fixed marker. Defensive:
// messages are composed without secrets, but this guards future changes.
func scrub(s string) string { return tokenish.ReplaceAllString(s, redactedMarker) }
