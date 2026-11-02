package runner

import "encoding/json"

// jsonUnmarshal is a thin wrapper to keep transfer.go decoupled from the json
// import site and ease future swapping (e.g. number handling).
func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
