package docker

import (
	"context"
	"io"

	"github.com/forgelab/backend/internal/logging"
)

// stripDockerTimestamp strips the RFC3339/RFC3339Nano timestamp prefix added by Docker's log streaming.
func stripDockerTimestamp(line string) string {
	return logging.StripDockerTimestamp(line)
}

// DemuxDockerStream reads a Docker multiplexed log stream (8-byte header frames)
// and emits individual lines for stdout and stderr to onLine.
// It stops cleanly when ctx is done or reader returns io.EOF.
func DemuxDockerStream(ctx context.Context, reader io.Reader, onLine func(stream string, line string)) error {
	return logging.DemuxDockerStream(ctx, reader, onLine)
}
