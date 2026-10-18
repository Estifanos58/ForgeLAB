package logging

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"

	// DefaultMaxLineLength is the maximum bytes per log line before flushing.
	// Prevents unbounded memory accumulation on newline-free container output.
	DefaultMaxLineLength = 64 * 1024 // 64 KB

	// DefaultMaxFrameSize is the maximum allowed frame payload before discarding.
	// Prevents allocating absurd buffers on corrupt or malicious frame headers.
	DefaultMaxFrameSize = 8 * 1024 * 1024 // 8 MB

	// StreamReadChunkSize is the chunk size used to read frame payloads.
	StreamReadChunkSize = 32 * 1024 // 32 KB
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
// Memory is strictly bounded to prevent desynchronization from oversized frames
// and OOM from newline-free output.
func DemuxDockerStream(ctx context.Context, reader io.Reader, onLine func(stream string, line string)) error {
	hdr := make([]byte, 8)
	chunkBuf := make([]byte, StreamReadChunkSize)
	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer

	flushLines := func(buf *bytes.Buffer, stream string) {
		for {
			idx := bytes.IndexByte(buf.Bytes(), '\n')
			if idx >= 0 {
				if idx > DefaultMaxLineLength {
					lineBytes := buf.Next(DefaultMaxLineLength)
					rem := idx + 1 - DefaultMaxLineLength
					_ = buf.Next(rem)
					line := StripDockerTimestamp(strings.TrimRight(string(lineBytes), "\r\n"))
					if line != "" {
						onLine(stream, line+" ... [truncated: exceeded 64KB log size limit]")
					}
					continue
				}
				lineBytes := buf.Next(idx + 1)
				line := StripDockerTimestamp(strings.TrimRight(string(lineBytes), "\r\n"))
				if line != "" {
					onLine(stream, line)
				}
			} else {
				if buf.Len() >= DefaultMaxLineLength {
					lineBytes := buf.Next(DefaultMaxLineLength)
					line := StripDockerTimestamp(strings.TrimRight(string(lineBytes), "\r\n"))
					if line != "" {
						onLine(stream, line)
					}
				} else {
					break
				}
			}
		}
	}

	flushRemaining := func(buf *bytes.Buffer, stream string) {
		for buf.Len() > 0 {
			toTake := buf.Len()
			if toTake > DefaultMaxLineLength {
				toTake = DefaultMaxLineLength
			}
			lineBytes := buf.Next(toTake)
			line := StripDockerTimestamp(strings.TrimRight(string(lineBytes), "\r\n"))
			if line != "" {
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
		payloadSize := binary.BigEndian.Uint32(hdr[4:8])
		if payloadSize == 0 {
			continue
		}

		// If frame payload exceeds max permitted frame size or stream type is unrecognized,
		// discard payload from reader to keep the multiplexed stream synchronized for subsequent frames.
		if payloadSize > DefaultMaxFrameSize || (streamType != 1 && streamType != 2) {
			if _, err := io.CopyN(io.Discard, reader, int64(payloadSize)); err != nil {
				if err == io.EOF || errors.Is(err, context.Canceled) {
					return nil
				}
				return err
			}
			continue
		}

		var targetBuf *bytes.Buffer
		var streamName string
		if streamType == 2 {
			targetBuf = &stderrBuf
			streamName = StreamStderr
		} else {
			targetBuf = &stdoutBuf
			streamName = StreamStdout
		}

		remaining := int64(payloadSize)
		for remaining > 0 {
			chunkLen := int64(len(chunkBuf))
			if remaining < chunkLen {
				chunkLen = remaining
			}
			n, err := io.ReadFull(reader, chunkBuf[:chunkLen])
			if err != nil {
				if err == io.EOF || errors.Is(err, context.Canceled) {
					return nil
				}
				return err
			}
			remaining -= int64(n)
			targetBuf.Write(chunkBuf[:n])
			flushLines(targetBuf, streamName)
		}
	}
}
