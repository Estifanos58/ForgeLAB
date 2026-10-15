package database_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/forgelab/backend/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegration_DatabaseAndRedis(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://forgelab:forgelab_dev_password@localhost:5432/forgelab?sslmode=disable"
	}
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379/0"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. PostgreSQL Integration
	pool, err := database.Connect(ctx, dbURL)
	if err != nil {
		if os.Getenv("REQUIRE_INTEGRATION_DB") != "" {
			t.Fatalf("Required PostgreSQL connection failed: %v", err)
		}
		t.Skipf("Skipping PostgreSQL integration test: %v", err)
	} else {
		defer pool.Close()
		var one int
		err = pool.QueryRow(ctx, "SELECT 1").Scan(&one)
		require.NoError(t, err)
		assert.Equal(t, 1, one)
	}

	// 2. Redis Integration
	rdb, err := database.ConnectRedis(ctx, redisURL)
	if err != nil {
		if os.Getenv("REQUIRE_INTEGRATION_DB") != "" {
			t.Fatalf("Required Redis connection failed: %v", err)
		}
		t.Skipf("Skipping Redis integration test: %v", err)
	} else {
		defer rdb.Close()
		err = rdb.Set(ctx, "test_key", "test_val", 10*time.Second).Err()
		require.NoError(t, err)
		val, err := rdb.Get(ctx, "test_key").Result()
		require.NoError(t, err)
		assert.Equal(t, "test_val", val)
	}
}
