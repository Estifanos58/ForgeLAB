package discovery

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDAG_CycleDetection_RejectsWithCyclePath(t *testing.T) {
	// A -> B -> C -> A
	nodes := []ServiceNode{
		{Name: "service-a", DependsOn: []string{"service-b"}},
		{Name: "service-b", DependsOn: []string{"service-c"}},
		{Name: "service-c", DependsOn: []string{"service-a"}},
	}

	_, tiers, err := ValidateAndBuildDAG(nodes)
	require.Error(t, err)
	assert.Nil(t, tiers)
	assert.Contains(t, err.Error(), "cyclic service dependency detected")
	// Must contain the cycle path details
	assert.Contains(t, err.Error(), "->")
}

func TestDAG_SelfDependency_RejectsImmediately(t *testing.T) {
	nodes := []ServiceNode{
		{Name: "web", DependsOn: []string{"web"}},
	}

	_, tiers, err := ValidateAndBuildDAG(nodes)
	require.Error(t, err)
	assert.Nil(t, tiers)
	assert.Contains(t, err.Error(), "self-dependency is not allowed")
}

func TestDAG_UnknownDependency_RejectsImmediately(t *testing.T) {
	nodes := []ServiceNode{
		{Name: "web", DependsOn: []string{"nonexistent-db"}},
	}

	_, tiers, err := ValidateAndBuildDAG(nodes)
	require.Error(t, err)
	assert.Nil(t, tiers)
	assert.Contains(t, err.Error(), "does not exist in declared services")
}

func TestDAG_UnsupportedDependencyCondition_RejectsImmediately(t *testing.T) {
	nodes := []ServiceNode{
		{
			Name:      "web",
			DependsOn: []string{"db"},
			DependsOnConditions: map[string]string{
				"db": "service_unknown_condition",
			},
		},
		{Name: "db"},
	}

	_, tiers, err := ValidateAndBuildDAG(nodes)
	require.Error(t, err)
	assert.Nil(t, tiers)
	assert.Contains(t, err.Error(), "unsupported dependency condition 'service_unknown_condition'")
}

func TestDAG_SupportedDependencyConditions_Accepted(t *testing.T) {
	conditions := []string{
		"service_started",
		"service_healthy",
		"service_completed_successfully",
	}

	for _, cond := range conditions {
		assert.True(t, IsSupportedDependencyCondition(cond), "condition %s should be supported", cond)
	}
	assert.False(t, IsSupportedDependencyCondition("invalid_cond"))

	nodes := []ServiceNode{
		{
			Name:      "web",
			DependsOn: []string{"db", "migrate"},
			DependsOnConditions: map[string]string{
				"db":      "service_healthy",
				"migrate": "service_completed_successfully",
			},
		},
		{Name: "db"},
		{Name: "migrate", DependsOn: []string{"db"}, DependsOnConditions: map[string]string{"db": "service_started"}},
	}

	_, tiers, err := ValidateAndBuildDAG(nodes)
	require.NoError(t, err)
	require.Len(t, tiers, 3)
	assert.Equal(t, []string{"db"}, tiers[0])
	assert.Equal(t, []string{"migrate"}, tiers[1])
	assert.Equal(t, []string{"web"}, tiers[2])
}

func TestDAG_DeterministicAlphabeticalKahnTiers(t *testing.T) {
	// Independent services in a tier must be deterministically sorted alphabetically
	nodes := []ServiceNode{
		{Name: "zebra"},
		{Name: "alpha"},
		{Name: "mike"},
		{Name: "bravo"},
	}

	_, tiers, err := ValidateAndBuildDAG(nodes)
	require.NoError(t, err)
	require.Len(t, tiers, 1)
	assert.Equal(t, []string{"alpha", "bravo", "mike", "zebra"}, tiers[0])
}

func TestDAG_GeneratePlan_NeverFallsBackOnCycles(t *testing.T) {
	// A DiscoveryResult containing a cycle must fail closed in GeneratePlan,
	// never silently falling back to putting all services in a single tier.
	discResult := &DiscoveryResult{
		Services: []DiscoveredService{
			{Name: "svc1", DependsOn: []string{"svc2"}},
			{Name: "svc2", DependsOn: []string{"svc1"}},
		},
	}

	plan, err := GeneratePlan(discResult, PlanOptions{})
	require.Error(t, err, "GeneratePlan must fail when DAG contains cycles")
	assert.Nil(t, plan)
	assert.Contains(t, err.Error(), "cyclic service dependency detected")
}
