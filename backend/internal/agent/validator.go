package agent

import "fmt"

const maxDAGNodes = 10

func ValidateDAG(plan *DAGPlan, availableTools []string) error {
	if plan == nil {
		return fmt.Errorf("dag validation failed: plan is nil")
	}
	if len(plan.Nodes) > maxDAGNodes {
		return fmt.Errorf("dag validation failed: node_count=%d exceeds maximum=%d", len(plan.Nodes), maxDAGNodes)
	}

	toolSet := make(map[string]struct{}, len(availableTools))
	for _, t := range availableTools {
		toolSet[t] = struct{}{}
	}

	indexByID := make(map[string]int, len(plan.Nodes))
	for idx, node := range plan.Nodes {
		if node.ID == "" {
			return fmt.Errorf("dag validation failed: node index=%d rule=missing_id", idx)
		}
		if _, exists := indexByID[node.ID]; exists {
			return fmt.Errorf("dag validation failed: node_id=%s rule=duplicate_id", node.ID)
		}
		indexByID[node.ID] = idx

		if node.Tool == "" {
			return fmt.Errorf("dag validation failed: node_id=%s rule=missing_tool", node.ID)
		}
		if _, ok := toolSet[node.Tool]; !ok {
			return fmt.Errorf("dag validation failed: node_id=%s rule=unknown_tool tool=%s", node.ID, node.Tool)
		}
	}

	for idx, node := range plan.Nodes {
		for _, depID := range node.DependsOn {
			depIdx, exists := indexByID[depID]
			if !exists {
				return fmt.Errorf("dag validation failed: node_id=%s rule=missing_dependency dependency=%s", node.ID, depID)
			}
			if depIdx > idx {
				return fmt.Errorf("dag validation failed: node_id=%s rule=forward_dependency dependency=%s", node.ID, depID)
			}
		}
	}

	adj := make(map[string][]string, len(plan.Nodes))
	for _, node := range plan.Nodes {
		for _, depID := range node.DependsOn {
			adj[depID] = append(adj[depID], node.ID)
		}
	}

	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)
	state := make(map[string]int, len(plan.Nodes))

	var dfs func(id string) error
	dfs = func(id string) error {
		switch state[id] {
		case visiting:
			return fmt.Errorf("dag validation failed: node_id=%s rule=cycle_detected", id)
		case visited:
			return nil
		}

		state[id] = visiting
		for _, next := range adj[id] {
			if err := dfs(next); err != nil {
				return err
			}
		}
		state[id] = visited
		return nil
	}

	for _, node := range plan.Nodes {
		if state[node.ID] == unvisited {
			if err := dfs(node.ID); err != nil {
				return err
			}
		}
	}

	return nil
}
