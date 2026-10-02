package logging

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
)

const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// StripDockerTimestamp strips the RFC3339/RFC3339Nano timestamp prefix added by Docker's log streaming.
// Format is typically "2006-01-02T15:04:05.999999999Z "
func StripDockerTimestamp(line string) string {
	if len(line) > 20 && line[4] == '-' && line[7] == '-' && line[10] == 'T' {
		if spaceIdx := strings.IndexByte(line, ' '); spaceIdx >= 19 && spaceIdx < 40 {
			return line[spaceIdx+1:]
		}
	}
	return line
}

// DemuxDockerStream reads a Docker multiplexed log stream (8-byte header frames)
// and emits individual lines for stdout and stderr to onLine.
// It stops cleanly when ctx is done or reader returns io.EOF.
func DemuxDockerStream(ctx context.Context, reader io.Reader, onLine func(stream string, line string)) error {
	hdr := make([]byte, 8)
	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer

	flushLines := func(buf *bytes.Buffer, stream string) {
		for {
			idx := bytes.IndexByte(buf.Bytes(), '\n')
			if idx < 0 {
				break
			}
			lineBytes := buf.Next(idx + 1)
			line := strings.TrimRight(string(lineBytes), "\r\n")
			if line != "" {
				line = StripDockerTimestamp(line)
				onLine(stream, line)
			}
		}
	}

	flushRemaining := func(buf *bytes.Buffer, stream string) {
		if buf.Len() > 0 {
			line := strings.TrimRight(buf.String(), "\r\n")
			buf.Reset()
			if line != "" {
				line = StripDockerTimestamp(line)
				onLine(stream, line)
			}
		}
	}

	defer func() {
		flushRemaining(&stdoutBuf, StreamStdout)
		flushRemaining(&stderrBuf, StreamStderr)
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_, err := io.ReadFull(reader, hdr)
		if err != nil {
			if err == io.EOF || errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}

		streamType := hdr[0]
		payloadSize := int(hdr[4])<<24 | int(hdr[5])<<16 | int(hdr[6])<<8 | int(hdr[7])
		if payloadSize <= 0 || payloadSize > 1024*1024 {
			continue
		}

		payload := make([]byte, payloadSize)
		_, err = io.ReadFull(reader, payload)
		if err != nil {
			if err == io.EOF || errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}

		if streamType == 2 {
			stderrBuf.Write(payload)
			flushLines(&stderrBuf, StreamStderr)
		} else {
			// Stream 1 (stdout) or other
			stdoutBuf.Write(payload)
			flushLines(&stdoutBuf, StreamStdout)
		}
	}
}
