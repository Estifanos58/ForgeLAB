package models_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
)

func TestValidateOwnerResourceQuota(t *testing.T) {
	t.Run("Zero limits allows any usage", func(t *testing.T) {
		active := []*models.Service{
			{ID: uuid.New(), ResourceConfig: models.ResourceConfig{CpuMillicores: 2000, MemoryMB: 4096}},
		}
		newSvcs := []*models.Service{
			{ID: uuid.New(), ResourceConfig: models.ResourceConfig{CpuMillicores: 2000, MemoryMB: 4096}},
		}
		err := models.ValidateOwnerResourceQuota(active, newSvcs, 0, 0)
		require.NoError(t, err)
	})

	t.Run("Within quota limits passes", func(t *testing.T) {
		active := []*models.Service{
			{ID: uuid.New(), ResourceConfig: models.ResourceConfig{CpuMillicores: 1000, MemoryMB: 1024}},
		}
		newSvcs := []*models.Service{
			{ID: uuid.New(), ResourceConfig: models.ResourceConfig{CpuMillicores: 1000, MemoryMB: 1024}},
		}
		err := models.ValidateOwnerResourceQuota(active, newSvcs, 4096, 4000)
		require.NoError(t, err)
	})

	t.Run("Exceeding owner memory quota fails with clear error", func(t *testing.T) {
		active := []*models.Service{
			{ID: uuid.New(), ResourceConfig: models.ResourceConfig{CpuMillicores: 1000, MemoryMB: 2048}},
		}
		newSvcs := []*models.Service{
			{ID: uuid.New(), ResourceConfig: models.ResourceConfig{CpuMillicores: 1000, MemoryMB: 3000}},
		}
		err := models.ValidateOwnerResourceQuota(active, newSvcs, 4096, 8000)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "owner memory quota exceeded")
		assert.Contains(t, err.Error(), "5048 MB")
	})

	t.Run("Exceeding owner CPU quota fails with clear error", func(t *testing.T) {
		active := []*models.Service{
			{ID: uuid.New(), ResourceConfig: models.ResourceConfig{CpuMillicores: 2000, MemoryMB: 1024}},
		}
		newSvcs := []*models.Service{
			{ID: uuid.New(), ResourceConfig: models.ResourceConfig{CpuMillicores: 3000, MemoryMB: 1024}},
		}
		err := models.ValidateOwnerResourceQuota(active, newSvcs, 8192, 4000)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "owner CPU quota exceeded")
		assert.Contains(t, err.Error(), "5000 millicores")
	})
}
