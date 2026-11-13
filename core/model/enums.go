package model

import "sort"

// AssertionOps are the assertion operators the project format defines
// (the same enum as the published JSON Schema). Each assertion type accepts
// the subset that makes sense for it.
var AssertionOps = map[string]bool{
	"eq": true, "ne": true, "lt": true, "lte": true, "gt": true, "gte": true,
	"contains": true, "not_contains": true, "matches": true, "not_matches": true,
	"exists": true, "not_exists": true,
}

// TransferSources are the property-transfer sources (transfer "from").
var TransferSources = map[string]bool{
	"jsonpath": true, "xpath": true, "header": true, "status": true, "body": true, "regex": true,
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
