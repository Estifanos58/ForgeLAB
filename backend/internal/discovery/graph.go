package discovery

import (
	"fmt"
	"sort"
	"strings"
)

// Supported dependency conditions matching Docker Compose specification
const (
	DepConditionStarted               = "service_started"
	DepConditionHealthy               = "service_healthy"
	DepConditionCompletedSuccessfully = "service_completed_successfully"
)

// SupportedDependencyConditions contains the set of valid dependency conditions.
var SupportedDependencyConditions = map[string]bool{
	DepConditionStarted:               true,
	DepConditionHealthy:               true,
	DepConditionCompletedSuccessfully: true,
}

// IsSupportedDependencyCondition checks whether a condition string is valid.
// An empty condition defaults to "service_started" and is considered valid.
func IsSupportedDependencyCondition(cond string) bool {
	if cond == "" {
		return true
	}
	return SupportedDependencyConditions[cond]
}

// ServiceNode represents a service with its declared dependencies for graph validation and tier computation.
type ServiceNode struct {
	Name                string
	DependsOn           []string
	DependsOnConditions map[string]string
}

// DependencyGraphError represents a validation or topological error in the dependency graph.
type DependencyGraphError struct {
	ServiceName string
	Dependency  string
	Reason      string
	Cycle       []string
}

func (e *DependencyGraphError) Error() string {
	if len(e.Cycle) > 0 {
		return fmt.Sprintf("dependency cycle detected: cyclic service dependency detected: %s", strings.Join(e.Cycle, " -> "))
	}
	if e.ServiceName != "" && e.Dependency != "" {
		return fmt.Sprintf("service '%s' dependency '%s': %s", e.ServiceName, e.Dependency, e.Reason)
	}
	if e.ServiceName != "" {
		return fmt.Sprintf("service '%s': %s", e.ServiceName, e.Reason)
	}
	return e.Reason
}

// ValidateAndBuildDAG performs authoritative dependency graph validation and partitions services into
// ordered, deterministic execution tiers.
//
// Validation rules:
// 1. Service names must be non-empty and unique (case-insensitive).
// 2. All dependencies in depends_on must exist in the declared services list (unknown service references rejected).
// 3. Self-dependencies (a service depending on itself) are rejected.
// 4. Any condition in depends_on_conditions must be a supported condition (service_started, service_healthy, service_completed_successfully).
// 5. Cyclic dependencies are detected and rejected with the exact cycle path.
// 6. Services are partitioned deterministically into tiers (Kahn's algorithm with alphabetical sorting per tier).
//
// Never silently ignores unknown dependencies, invalid conditions, or cycles.
func ValidateAndBuildDAG(services []ServiceNode) (map[string][]string, [][]string, error) {
	if len(services) == 0 {
		return make(map[string][]string), nil, nil
	}

	canonicalNameMap := make(map[string]string, len(services)) // lowerName -> exactName
	for _, s := range services {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			return nil, nil, &DependencyGraphError{
				Reason: "service name cannot be empty",
			}
		}
		lower := strings.ToLower(name)
		if existing, exists := canonicalNameMap[lower]; exists {
			return nil, nil, &DependencyGraphError{
				ServiceName: name,
				Reason:      fmt.Sprintf("duplicate service name '%s' (conflicts with '%s')", name, existing),
			}
		}
		canonicalNameMap[lower] = name
	}

	depMap := make(map[string][]string, len(services))
	for _, s := range services {
		canonicalServiceName := canonicalNameMap[strings.ToLower(strings.TrimSpace(s.Name))]
		var validDeps []string
		seenDeps := make(map[string]bool)

		for _, dep := range s.DependsOn {
			trimmedDep := strings.TrimSpace(dep)
			if trimmedDep == "" {
				continue
			}
			lowerDep := strings.ToLower(trimmedDep)

			// Rule 3: Reject self-dependency
			if lowerDep == strings.ToLower(canonicalServiceName) {
				return nil, nil, &DependencyGraphError{
					ServiceName: canonicalServiceName,
					Dependency:  dep,
					Reason:      "self-dependency is not allowed",
				}
			}

			// Rule 2: Reject unknown service reference
			targetCanonical, exists := canonicalNameMap[lowerDep]
			if !exists {
				return nil, nil, &DependencyGraphError{
					ServiceName: canonicalServiceName,
					Dependency:  dep,
					Reason:      fmt.Sprintf("dependency '%s' does not exist in declared services (dependency '%s' does not exist in deployment plan)", dep, dep),
				}
			}

			// Rule 4: Reject unsupported dependency condition
			if s.DependsOnConditions != nil {
				if cond, hasCond := s.DependsOnConditions[dep]; hasCond && cond != "" {
					if !IsSupportedDependencyCondition(cond) {
						return nil, nil, &DependencyGraphError{
							ServiceName: canonicalServiceName,
							Dependency:  dep,
							Reason:      fmt.Sprintf("unsupported dependency condition '%s' (must be service_started, service_healthy, or service_completed_successfully)", cond),
						}
					}
				}
				// Also check matching against target canonical name if keyed differently
				if cond, hasCond := s.DependsOnConditions[targetCanonical]; hasCond && cond != "" {
					if !IsSupportedDependencyCondition(cond) {
						return nil, nil, &DependencyGraphError{
							ServiceName: canonicalServiceName,
							Dependency:  targetCanonical,
							Reason:      fmt.Sprintf("unsupported dependency condition '%s' (must be service_started, service_healthy, or service_completed_successfully)", cond),
						}
					}
				}
			}

			if !seenDeps[targetCanonical] {
				seenDeps[targetCanonical] = true
				validDeps = append(validDeps, targetCanonical)
			}
		}

		// Also check any orphan entries in DependsOnConditions for services not even in depends_on
		if s.DependsOnConditions != nil {
			for condService, cond := range s.DependsOnConditions {
				lowerCondService := strings.ToLower(strings.TrimSpace(condService))
				if _, exists := canonicalNameMap[lowerCondService]; !exists {
					return nil, nil, &DependencyGraphError{
						ServiceName: canonicalServiceName,
						Dependency:  condService,
						Reason:      fmt.Sprintf("depends_on_conditions references unknown service '%s'", condService),
					}
				}
				if !IsSupportedDependencyCondition(cond) {
					return nil, nil, &DependencyGraphError{
						ServiceName: canonicalServiceName,
						Dependency:  condService,
						Reason:      fmt.Sprintf("unsupported dependency condition '%s'", cond),
					}
				}
			}
		}

		sort.Strings(validDeps)
		depMap[canonicalServiceName] = validDeps
	}

	// Rule 5: Detect cycles using DFS with full cycle path
	if cycle := detectGraphCycle(depMap); len(cycle) > 0 {
		return depMap, nil, &DependencyGraphError{
			Cycle: cycle,
		}
	}

	// Rule 6: Deterministic tier computation using Kahn's algorithm
	inDegree := make(map[string]int, len(services))
	adj := make(map[string][]string, len(services)) // dep -> services that depend on dep

	for name, deps := range depMap {
		inDegree[name] = len(deps)
		for _, dep := range deps {
			adj[dep] = append(adj[dep], name)
		}
	}

	var tiers [][]string
	remaining := len(depMap)

	for remaining > 0 {
		var currentTier []string
		for name, deg := range inDegree {
			if deg == 0 {
				currentTier = append(currentTier, name)
			}
		}

		if len(currentTier) == 0 {
			// Cycle detected (failsafe, though detectGraphCycle catches it)
			return depMap, nil, fmt.Errorf("cyclic service dependency detected in Compose definition")
		}

		sort.Strings(currentTier)
		tiers = append(tiers, currentTier)

		for _, name := range currentTier {
			delete(inDegree, name)
			remaining--
			for _, dependent := range adj[name] {
				if _, ok := inDegree[dependent]; ok {
					inDegree[dependent]--
				}
			}
		}
	}

	return depMap, tiers, nil
}

// detectGraphCycle finds directed cycles in a dependency graph and returns the cycle path if found.
func detectGraphCycle(depMap map[string][]string) []string {
	// Sort node names for deterministic traversal
	nodes := make([]string, 0, len(depMap))
	for n := range depMap {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)

	visited := make(map[string]int) // 0: unvisited, 1: visiting, 2: visited
	var path []string

	var dfs func(node string) []string
	dfs = func(node string) []string {
		visited[node] = 1
		path = append(path, node)

		for _, neighbor := range depMap[node] {
			if visited[neighbor] == 1 {
				// Found back-edge (cycle)
				cycleStart := 0
				for i, p := range path {
					if p == neighbor {
						cycleStart = i
						break
					}
				}
				cycle := append([]string{}, path[cycleStart:]...)
				cycle = append(cycle, neighbor)
				return cycle
			}
			if visited[neighbor] == 0 {
				if cycle := dfs(neighbor); len(cycle) > 0 {
					return cycle
				}
			}
		}

		visited[node] = 2
		path = path[:len(path)-1]
		return nil
	}

	for _, n := range nodes {
		if visited[n] == 0 {
			if cycle := dfs(n); len(cycle) > 0 {
				return cycle
			}
		}
	}
	return nil
}
