package envparser

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode"
)

const (
	// MaxFileSize is the maximum allowed .env file size (512 KB).
	MaxFileSize = 512 * 1024
	// MaxVariables is the maximum number of variables parsed from a single .env file.
	MaxVariables = 500
	// MaxKeyLength is the maximum allowed key name length.
	MaxKeyLength = 256
	// MaxValueLength is the maximum allowed value length.
	MaxValueLength = 8192
)

var (
	ErrFileTooLarge     = errors.New(".env file exceeds maximum size limit (512KB)")
	ErrTooManyVariables = errors.New(".env file exceeds maximum variable count (500)")
	varNameRegex        = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// ParseFile reads and parses a .env file from disk safely adhering to all resource limits.
// Returns an empty map without error if the file does not exist.
func ParseFile(path string) (map[string]string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]string), nil
		}
		return nil, fmt.Errorf("failed to access .env file: %w", err)
	}

	if fi.IsDir() {
		return nil, errors.New(".env path is a directory")
	}

	if fi.Size() > MaxFileSize {
		return nil, ErrFileTooLarge
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open .env file: %w", err)
	}
	defer f.Close()

	return Parse(io.LimitReader(f, MaxFileSize+1))
}

// Parse parses environment variables from an io.Reader.
// Adheres strictly to security limits: no shell expansion, no command evaluation.
func Parse(r io.Reader) (map[string]string, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read .env content: %w", err)
	}
	if len(data) > MaxFileSize {
		return nil, ErrFileTooLarge
	}

	return ParseBytes(data)
}

// ParseBytes parses environment variables from a byte slice.
func ParseBytes(data []byte) (map[string]string, error) {
	result := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(data))

	// Allow buffer up to MaxValueLength + MaxKeyLength + 256
	buf := make([]byte, 16384)
	scanner.Buffer(buf, MaxValueLength+MaxKeyLength+1024)

	varCount := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Strip optional "export " prefix
		if strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "export\t") {
			line = strings.TrimSpace(line[6:])
		}

		eqIdx := strings.Index(line, "=")
		if eqIdx < 0 {
			// Malformed line without '=', skip safely
			continue
		}

		rawKey := strings.TrimSpace(line[:eqIdx])
		if rawKey == "" || len(rawKey) > MaxKeyLength {
			continue
		}

		if !varNameRegex.MatchString(rawKey) {
			// Malformed variable name (e.g. contains special characters or shell syntax)
			continue
		}

		if varCount >= MaxVariables {
			return nil, ErrTooManyVariables
		}

		rawValue := strings.TrimSpace(line[eqIdx+1:])
		val := parseValue(rawValue)
		if len(val) > MaxValueLength {
			val = val[:MaxValueLength]
		}

		result[rawKey] = val
		varCount++
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading .env content: %w", err)
	}

	return result, nil
}

// parseValue safely extracts the literal value from a line, handling quotes, escapes, and inline comments.
// Never evaluates shell expressions, commands ($(...)), or backticks.
func parseValue(val string) string {
	if val == "" {
		return ""
	}

	// Double-quoted value
	if strings.HasPrefix(val, "\"") {
		var buf bytes.Buffer
		escaped := false
		closed := false
		for i := 1; i < len(val); i++ {
			c := val[i]
			if escaped {
				switch c {
				case 'n':
					buf.WriteByte('\n')
				case 'r':
					buf.WriteByte('\r')
				case 't':
					buf.WriteByte('\t')
				case '"':
					buf.WriteByte('"')
				case '\\':
					buf.WriteByte('\\')
				case '$':
					buf.WriteByte('$')
				default:
					buf.WriteByte('\\')
					buf.WriteByte(c)
				}
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				closed = true
				break
			} else {
				buf.WriteByte(c)
			}
		}
		if closed {
			return buf.String()
		}
		// If closing quote wasn't found, return as-is without opening quote
		return buf.String()
	}

	// Single-quoted value: literal until closing single quote
	if strings.HasPrefix(val, "'") {
		endIdx := strings.Index(val[1:], "'")
		if endIdx >= 0 {
			return val[1 : 1+endIdx]
		}
		return val[1:]
	}

	// Unquoted value: strip trailing inline comments
	// An inline comment begins with whitespace followed by '#'
	commentIdx := -1
	for i := 0; i < len(val); i++ {
		if val[i] == '#' {
			if i > 0 && unicode.IsSpace(rune(val[i-1])) {
				commentIdx = i
				break
			}
		}
	}
	if commentIdx >= 0 {
		val = strings.TrimSpace(val[:commentIdx])
	}

	return val
}
