package main

import "encoding/json"

// jsonUnmarshal is a thin wrapper around json.Unmarshal used in keeperd to
// decode ctl payload bytes into typed structs.
func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
