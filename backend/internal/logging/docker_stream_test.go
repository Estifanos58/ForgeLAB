package logging_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"testing"
	"time"

	"github.com/forgelab/backend/internal/logging"
)

func makeDockerFrame(streamType byte, payload []byte) []byte {
	hdr := make([]byte, 8)
	hdr[0] = streamType
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload)))
	return append(hdr, payload...)
}

func TestDemuxDockerStream_MultiLineStdoutStderr(t *testing.T) {
	// Simulate a Docker multiplexed stream where a single frame contains multiple lines
	var buf bytes.Buffer

	// Multi-line stdout frame with RFC3339Nano timestamps
	stdoutPayload := []byte("2026-10-02T07:23:18.100000000Z Server starting...\n2026-10-02T07:23:18.200000000Z Listening on port 3000\r\n2026-10-02T07:23:18.300000000Z DB connected\n")
	buf.Write(makeDockerFrame(1, stdoutPayload))

	// Multi-line stderr frame
	stderrPayload := []byte("2026-10-02T07:23:18.400000000Z [WARN] Cache miss\n2026-10-02T07:23:18.500000000Z [ERROR] Rate limit reached\n")
	buf.Write(makeDockerFrame(2, stderrPayload))

	type logLine struct {
		stream  string
		message string
	}
	var received []logLine

	ctx := context.Background()
	err := logging.DemuxDockerStream(ctx, &buf, func(stream string, line string) {
		received = append(received, logLine{stream: stream, message: line})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []logLine{
		{stream: logging.StreamStdout, message: "Server starting..."},
		{stream: logging.StreamStdout, message: "Listening on port 3000"},
		{stream: logging.StreamStdout, message: "DB connected"},
		{stream: logging.StreamStderr, message: "[WARN] Cache miss"},
		{stream: logging.StreamStderr, message: "[ERROR] Rate limit reached"},
	}

	if len(received) != len(expected) {
		t.Fatalf("expected %d lines, got %d: %+v", len(expected), len(received), received)
	}

	for i, exp := range expected {
		if received[i].stream != exp.stream || received[i].message != exp.message {
			t.Errorf("line %d: got {stream:%q, message:%q}, want {stream:%q, message:%q}",
				i, received[i].stream, received[i].message, exp.stream, exp.message)
		}
	}
}

func TestDemuxDockerStream_PartialFrameBuffering(t *testing.T) {
	var buf bytes.Buffer

	// Frame 1 has partial line
	buf.Write(makeDockerFrame(1, []byte("Hello ")))
	// Frame 2 finishes the line
	buf.Write(makeDockerFrame(1, []byte("World!\n")))

	var lines []string
	ctx := context.Background()
	err := logging.DemuxDockerStream(ctx, &buf, func(stream string, line string) {
		lines = append(lines, line)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(lines) != 1 || lines[0] != "Hello World!" {
		t.Fatalf("expected single reassembled line 'Hello World!', got: %+v", lines)
	}
}

func TestDemuxDockerStream_EOFFlush(t *testing.T) {
	var buf bytes.Buffer

	// Frame with no trailing newline at container exit
	buf.Write(makeDockerFrame(1, []byte("Process exited with code 0")))

	var lines []string
	ctx := context.Background()
	err := logging.DemuxDockerStream(ctx, &buf, func(stream string, line string) {
		lines = append(lines, line)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(lines) != 1 || lines[0] != "Process exited with code 0" {
		t.Fatalf("expected flushed line on EOF, got: %+v", lines)
	}
}

func TestDemuxDockerStream_CancellableContext(t *testing.T) {
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- logging.DemuxDockerStream(ctx, pr, func(stream string, line string) {})
	}()

	// Write one frame
	pw.Write(makeDockerFrame(1, []byte("log 1\n")))

	// Cancel context to simulate lifecycle cancellation
	cancel()
	_ = pw.Close()

	select {
	case <-errCh:
		// Clean exit without hanging
	case <-time.After(2 * time.Second):
		t.Fatal("DemuxDockerStream failed to terminate cleanly on context cancellation (goroutine leak)")
	}
}

func TestStripDockerTimestamp(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "2026-10-02T07:23:18.123456789Z Ready",
			expected: "Ready",
		},
		{
			input:    "2026-10-02T07:23:18Z Started",
			expected: "Started",
		},
		{
			input:    "2026-10-02T07:23:18+00:00 OK",
			expected: "OK",
		},
		{
			input:    "No timestamp here",
			expected: "No timestamp here",
		},
		{
			input:    "    at Object.<anonymous> (index.js:5:1)",
			expected: "    at Object.<anonymous> (index.js:5:1)",
		},
	}

	for _, tt := range tests {
		got := logging.StripDockerTimestamp(tt.input)
		if got != tt.expected {
			t.Errorf("StripDockerTimestamp(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
