package logging_test

import (
	"strings"
	"testing"

	"github.com/forgelab/backend/internal/logging"
)

func TestSecretRedactionInLogs(t *testing.T) {
	redactor := logging.NewLogRedactor()
	redactor.SetSecrets([]string{"super-secret-password-123", "API_KEY_9999"})

	logLine := "Connecting to database with password super-secret-password-123 and header API_KEY_9999"
	redacted := redactor.Redact(logLine)

	if strings.Contains(redacted, "super-secret-password-123") {
		t.Errorf("log line failed to redact password!")
	}
	if strings.Contains(redacted, "API_KEY_9999") {
		t.Errorf("log line failed to redact API key!")
	}

	if !strings.Contains(redacted, "[REDACTED]") {
		t.Errorf("expected [REDACTED] placeholder in redacted log")
	}
}
