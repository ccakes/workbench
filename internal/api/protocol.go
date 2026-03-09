package api

import "encoding/json"

// Request is a JSON request sent by the client.
type Request struct {
	Method string           `json:"method"`
	Params *json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON response sent by the server.
type Response struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}
