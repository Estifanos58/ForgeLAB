package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildPhaseClause(t *testing.T) {
	t.Run("all or empty yields empty clause", func(t *testing.T) {
		argIndex := 1
		var args []interface{}
		clause := buildPhaseClause("", &argIndex, &args)
		assert.Empty(t, clause)
		assert.Equal(t, 1, argIndex)
		assert.Empty(t, args)

		clause = buildPhaseClause("all", &argIndex, &args)
		assert.Empty(t, clause)
		assert.Equal(t, 1, argIndex)
		assert.Empty(t, args)

		clause = buildPhaseClause("ALL", &argIndex, &args)
		assert.Empty(t, clause)
		assert.Equal(t, 1, argIndex)
		assert.Empty(t, args)
	})

	t.Run("deployment yields deployment phases clause", func(t *testing.T) {
		argIndex := 1
		var args []interface{}
		clause := buildPhaseClause("deployment", &argIndex, &args)
		assert.Equal(t, " AND phase IN ('source', 'build', 'startup', 'health')", clause)
		assert.Equal(t, 1, argIndex)
		assert.Empty(t, args)
	})

	t.Run("production yields runtime phase clause", func(t *testing.T) {
		argIndex := 1
		var args []interface{}
		clause := buildPhaseClause("production", &argIndex, &args)
		assert.Equal(t, " AND phase = 'runtime'", clause)
		assert.Equal(t, 1, argIndex)
		assert.Empty(t, args)
	})

	t.Run("custom specific phase appends parameter", func(t *testing.T) {
		argIndex := 2
		var args []interface{}
		clause := buildPhaseClause("startup", &argIndex, &args)
		assert.Equal(t, " AND phase = $2", clause)
		assert.Equal(t, 3, argIndex)
		assert.Equal(t, []interface{}{"startup"}, args)
	})
}
