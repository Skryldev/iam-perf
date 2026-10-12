package config

import (
	"bytes"
	"encoding/json"
)

// secretKeys are fields that must never appear in resolved-config output.
// The schema itself stores no secrets (only secret *references*), but we
// redact defensively so future fields cannot leak through rendering.
var secretKeys = map[string]struct{}{
	"password":             {},
	"password_env":         {},
	"password_file":        {},
	"username":             {},
	"authorization_header": {},
	"cookie":               {},
	"token":                {},
}

// RedactedJSON renders the fully-resolved configuration as JSON with every
// secret-bearing field replaced by a fixed marker. Safe for logs and terminals.
func (c *Config) RedactedJSON() ([]byte, error) {
	var m map[string]any
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	redactMap(m)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func redactMap(m map[string]any) {
	for k, v := range m {
		if _, ok := secretKeys[k]; ok {
			m[k] = "[REDACTED]"
			continue
		}
		switch t := v.(type) {
		case map[string]any:
			redactMap(t)
		case []any:
			for _, e := range t {
				if em, ok := e.(map[string]any); ok {
					redactMap(em)
				}
			}
		}
	}
}
