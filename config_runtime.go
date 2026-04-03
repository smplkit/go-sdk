package smplkit

// chainEntry holds a single config node's resolved data for inheritance walking.
type chainEntry struct {
	ID           string
	Values       map[string]interface{}
	Environments map[string]map[string]interface{}
}

// deepMerge recursively merges override onto base.
// Both-dict keys are merged recursively; other types use the override value.
func deepMerge(base, override map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(base)+len(override))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range override {
		if existing, ok := result[k]; ok {
			if existingMap, ok1 := existing.(map[string]interface{}); ok1 {
				if overrideMap, ok2 := v.(map[string]interface{}); ok2 {
					result[k] = deepMerge(existingMap, overrideMap)
					continue
				}
			}
		}
		result[k] = v
	}
	return result
}

// resolveChain computes the final resolved cache from a parent chain.
// chain is ordered child→root; we walk root→child for inheritance.
// For each node: merge base values with environment-specific values, then
// merge onto the accumulated result (child wins over parent).
func resolveChain(chain []chainEntry, environment string) map[string]interface{} {
	result := make(map[string]interface{})
	for i := len(chain) - 1; i >= 0; i-- {
		entry := chain[i]

		// Start with base values for this node.
		nodeVals := make(map[string]interface{}, len(entry.Values))
		for k, v := range entry.Values {
			nodeVals[k] = v
		}

		// Overlay environment-specific values if present.
		if environment != "" {
			if envEntry, ok := entry.Environments[environment]; ok {
				if vals, ok := envEntry["values"]; ok {
					if valsMap, ok := vals.(map[string]interface{}); ok {
						nodeVals = deepMerge(nodeVals, valsMap)
					}
				}
			}
		}

		// Merge this node's resolved values onto the accumulated result.
		result = deepMerge(result, nodeVals)
	}
	return result
}
