package messaging

import "encoding/json"

func decodeJSON(data string, v any) error {
	return json.Unmarshal([]byte(data), v)
}
