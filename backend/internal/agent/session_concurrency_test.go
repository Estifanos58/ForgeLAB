package agent

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAgentSession_ConcurrentValidationVsConsumption proves that concurrent
// validation and consumption of an agent session are race-free and deterministic.
func TestAgentSession_ConcurrentValidationVsConsumption(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	userID := uuid.New()
	sess, err := sm.CreateSession(userID, "test-agent-1", time.Minute)
	require.NoError(t, err)

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines + 1)

	// Goroutine that marks session consumed
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		_ = sm.MarkConsumed(sess.ID)
	}()

	// Multiple goroutines validating the token concurrently
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s, err := sm.ValidateToken(sess.Token)
				if err == nil {
					assert.False(t, s.Consumed)
					assert.Equal(t, sess.ID, s.ID)
				} else {
					assert.ErrorIs(t, err, ErrSessionConsumed)
				}
			}
		}()
	}

	wg.Wait()
}

// TestAgentSession_ConcurrentValidationVsSourceBinding proves that concurrent
// validation and binding of a source to an agent session is completely race-free.
func TestAgentSession_ConcurrentValidationVsSourceBinding(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	userID := uuid.New()
	sess, err := sm.CreateSession(userID, "test-agent-2", time.Minute)
	require.NoError(t, err)

	sourceID := uuid.New()
	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines + 1)

	// Goroutine that binds source
	go func() {
		defer wg.Done()
		time.Sleep(2 * time.Millisecond)
		_, _ = sm.BindSource(sess.Token, sourceID, "test-folder", "test-agent-2")
	}()

	// Goroutines validating token
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s, err := sm.ValidateToken(sess.Token)
				if err == nil {
					// Safe immutable snapshot check
					if s.SourceID != nil {
						assert.Equal(t, sourceID, *s.SourceID)
					}
				}
			}
		}()
	}

	wg.Wait()
}

// TestAgentSession_ConcurrentSourceVerificationVsConsumption proves that concurrent
// source verification and consumption do not cause race conditions.
func TestAgentSession_ConcurrentSourceVerificationVsConsumption(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	userID := uuid.New()
	sourceID := uuid.New()
	sess, err := sm.CreateSession(userID, "test-agent-3", time.Minute)
	require.NoError(t, err)

	_, err = sm.BindSource(sess.Token, sourceID, "folder", "test-agent-3")
	require.NoError(t, err)

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines + 1)

	// Consume session concurrently
	go func() {
		defer wg.Done()
		time.Sleep(3 * time.Millisecond)
		_ = sm.MarkConsumed(sess.ID)
	}()

	// Verify token for source concurrently
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s, err := sm.VerifyTokenForSource(sess.Token, sourceID, "test-agent-3")
				if err == nil {
					assert.Equal(t, sourceID, *s.SourceID)
				}
			}
		}()
	}

	wg.Wait()
}

// TestAgentSession_ConcurrentSourceVerificationVsBinding proves that concurrent
// source verification and binding across competing callers is race-safe.
func TestAgentSession_ConcurrentSourceVerificationVsBinding(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	userID := uuid.New()
	sess, err := sm.CreateSession(userID, "test-agent-4", time.Minute)
	require.NoError(t, err)

	targetSource := uuid.New()
	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines + 1)

	go func() {
		defer wg.Done()
		time.Sleep(2 * time.Millisecond)
		_, _ = sm.BindSource(sess.Token, targetSource, "src", "test-agent-4")
	}()

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s, err := sm.VerifyTokenForSource(sess.Token, targetSource, "test-agent-4")
				if err == nil {
					assert.Equal(t, targetSource, *s.SourceID)
				}
			}
		}()
	}

	wg.Wait()
}
