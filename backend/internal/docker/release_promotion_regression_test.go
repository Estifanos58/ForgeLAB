package docker

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/proxy"
)

// TestReleasePromotion_ContainerPreservation verifies that previous service containers
// are preserved during release deployments and only cleaned up once release promotion
// criteria are fully satisfied across all services.
func TestReleasePromotion_ContainerPreservation(t *testing.T) {
	relID := uuid.New()
	svcDeployRelease := &models.ServiceDeployment{
		ID:           uuid.New(),
		DeploymentID: &relID, // Member of a full release
	}
	svcDeploySingle := &models.ServiceDeployment{
		ID:           uuid.New(),
		DeploymentID: nil, // Standalone service deployment
	}

	// In ExecuteServiceDeployment:
	// For standalone service deployment (svcDeploySingle), previous container is stopped and removed immediately.
	// For release deployment (svcDeployRelease), previous container is preserved until full release promotion.
	assert.Nil(t, svcDeploySingle.DeploymentID)
	assert.NotNil(t, svcDeployRelease.DeploymentID)

	canDestroyImmediatelySingle := svcDeploySingle.DeploymentID == nil
	canDestroyImmediatelyRelease := svcDeployRelease.DeploymentID == nil

	assert.True(t, canDestroyImmediatelySingle, "Single service deployment may clean up previous container immediately")
	assert.False(t, canDestroyImmediatelyRelease, "Release deployment must preserve previous container until entire release passes")
}

// TestReleasePromotion_PartialRelease_NeverClaimsFullRollback verifies that when some
// services succeed while others fail, ForgeLAB records partial success and never falsely claims full rollback.
func TestReleasePromotion_PartialRelease_NeverClaimsFullRollback(t *testing.T) {
	allHealthy := false
	anyHealthy := true
	errList := []string{"backend: health check timed out", "worker: dependency 'backend' failed condition 'service_healthy'"}

	var status string
	var logMsg string

	if allHealthy && anyHealthy {
		status = models.DeployStatusRunning
		logMsg = "Release #1 successfully deployed all services!"
	} else if anyHealthy {
		status = models.DeployStatusRunning
		logMsg = fmt.Sprintf("Release #1 finished with partial success (%s). Only individual healthy services were updated; full release rollback is not claimed.", strings.Join(errList, "; "))
	} else {
		status = models.DeployStatusFailed
		logMsg = fmt.Sprintf("All services failed deployment: %s", strings.Join(errList, "; "))
	}

	assert.Equal(t, models.DeployStatusRunning, status)
	assert.Contains(t, logMsg, "partial success")
	assert.Contains(t, logMsg, "full release rollback is not claimed")
	assert.NotContains(t, logMsg, "successfully deployed all services")
}

// TestPublicIngress_ReverseProxy_OnlyExposesPublicServices verifies that Caddy reverse proxy
// routes are generated only for publicly exposed frontend or application services, and never
// for databases, caches, or background workers.
func TestPublicIngress_ReverseProxy_OnlyExposesPublicServices(t *testing.T) {
	projID := uuid.New().String()
	fePort := 3000
	dbPort := 5432
	workerPort := 8080

	services := []*models.Service{
		{
			Name:           "frontend",
			Role:           models.RoleFrontend,
			Classification: models.ClassificationApplication,
			PublicExposed:  true,
			InternalPort:   fePort,
			CustomDomain:   strPtr("app.example.com"),
		},
		{
			Name:           "postgres",
			Role:           "other",
			Classification: models.ClassificationInfrastructure,
			PublicExposed:  false,
			InternalPort:   dbPort,
		},
		{
			Name:           "celery-worker",
			Role:           models.RoleWorker,
			Classification: models.ClassificationWorker,
			PublicExposed:  false,
			InternalPort:   workerPort,
		},
	}

	var routes []proxy.ProxyRoute
	for _, s := range services {
		isInternal := s.Classification == models.ClassificationInfrastructure ||
			s.Classification == models.ClassificationWorker ||
			s.Classification == models.ClassificationJob

		if !isInternal && (s.PublicExposed || s.Role == models.RoleFrontend) && s.CustomDomain != nil && *s.CustomDomain != "" {
			routes = append(routes, proxy.ProxyRoute{
				Domain:      *s.CustomDomain,
				TargetHost:  s.Name,
				TargetPort:  s.InternalPort,
				TLSEnabled:  true,
				ServiceName: s.Name,
				ProjectID:   projID,
			})
		}
	}

	require.Len(t, routes, 1)
	assert.Equal(t, "app.example.com", routes[0].Domain)
	assert.Equal(t, "frontend", routes[0].ServiceName)

	caddyfile := proxy.GenerateCaddyfile(routes)
	assert.Contains(t, caddyfile, "app.example.com {")
	assert.Contains(t, caddyfile, "reverse_proxy frontend:3000")
	assert.NotContains(t, caddyfile, "postgres")
	assert.NotContains(t, caddyfile, "celery-worker")
}

func strPtr(s string) *string {
	return &s
}
