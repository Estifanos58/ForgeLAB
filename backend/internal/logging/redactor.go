package logging

import (
	"strings"
	"sync"
)

// LogRedactor replaces secret values with [REDACTED] in log messages.
type LogRedactor struct {
	mu           sync.RWMutex
	secretValues []string
}

func NewLogRedactor() *LogRedactor {
	return &LogRedactor{
		secretValues: make([]string, 0),
	}
}

// SetSecrets sets the active list of secret values to redact.
func (r *LogRedactor) SetSecrets(secrets []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	valid := make([]string, 0, len(secrets))
	for _, s := range secrets {
		trimmed := strings.TrimSpace(s)
		if len(trimmed) >= 3 { // only redact meaningful values to prevent blanking common short strings
			valid = append(valid, trimmed)
		}
	}
	r.secretValues = valid
}

// AddSecrets appends additional secret values to the active list of values to redact.
func (r *LogRedactor) AddSecrets(secrets ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, s := range secrets {
		trimmed := strings.TrimSpace(s)
		if len(trimmed) >= 3 {
			r.secretValues = append(r.secretValues, trimmed)
		}
	}
}

// AddSecret appends a single secret value to the active list of values to redact.
func (r *LogRedactor) AddSecret(secret string) {
	r.AddSecrets(secret)
}

// Redact replaces any occurrence of configured secret values in message with [REDACTED].
func (r *LogRedactor) Redact(message string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.secretValues) == 0 || message == "" {
		return message
	}

	result := message
	for _, secret := range r.secretValues {
		if secret != "" && strings.Contains(result, secret) {
			result = strings.ReplaceAll(result, secret, "[REDACTED]")
		}
	}

	return result
}
