package docker

import (
	"time"

	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/logging"
)

// RuntimeCollectorParams holds parameters for attaching a continuous runtime log collector.
type RuntimeCollectorParams struct {
	ContainerID         string
	ProjectID           uuid.UUID
	DeploymentID        *uuid.UUID // release deployment ID or legacy deployment ID
	ServiceDeploymentID *uuid.UUID // nil for legacy deployment
	ServiceID           *uuid.UUID // nil for legacy deployment
	ServiceName         string     // for service badge
	Redactor            *logging.LogRedactor
	Since               time.Time
}
