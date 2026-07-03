package yandex

import "github.com/dakhar/yandex2mqtt/internal/device"

// --- get-devices ---

// DevicesResponse is the GET /v1.0/user/devices response.
type DevicesResponse struct {
	RequestID string         `json:"request_id"`
	Payload   DevicesPayload `json:"payload"`
}

type DevicesPayload struct {
	UserID  string              `json:"user_id"`
	Devices []device.Definition `json:"devices"`
}

// --- query ---

// QueryRequest is the POST /v1.0/user/devices/query request.
type QueryRequest struct {
	Devices []QueryRequestDevice `json:"devices"`
}

type QueryRequestDevice struct {
	ID         string         `json:"id"`
	CustomData map[string]any `json:"custom_data,omitempty"`
}

// QueryResponse is the query response.
type QueryResponse struct {
	RequestID string       `json:"request_id"`
	Payload   QueryPayload `json:"payload"`
}

type QueryPayload struct {
	Devices []device.QueryResult `json:"devices"`
}

// --- action ---

// ActionRequest is the POST /v1.0/user/devices/action request.
type ActionRequest struct {
	Payload ActionRequestPayload `json:"payload"`
}

type ActionRequestPayload struct {
	Devices []ActionRequestDevice `json:"devices"`
}

type ActionRequestDevice struct {
	ID           string             `json:"id"`
	CustomData   map[string]any     `json:"custom_data,omitempty"`
	Capabilities []ActionCapability `json:"capabilities"`
}

type ActionCapability struct {
	Type  string             `json:"type"`
	State ActionRequestState `json:"state"`
}

type ActionRequestState struct {
	Instance string `json:"instance"`
	Value    any    `json:"value"`
	Relative bool   `json:"relative,omitempty"`
}

// ActionResponse is the action response.
type ActionResponse struct {
	RequestID string        `json:"request_id"`
	Payload   ActionPayload `json:"payload"`
}

type ActionPayload struct {
	Devices []ActionDeviceResult `json:"devices"`
}

// ActionDeviceResult carries either per-capability results or a device-level
// action_result (when the whole device failed, e.g. DEVICE_NOT_FOUND).
type ActionDeviceResult struct {
	ID           string                   `json:"id"`
	Capabilities []device.ActionCapResult `json:"capabilities,omitempty"`
	ActionResult *device.ActionResult     `json:"action_result,omitempty"`
}

// --- unlink ---

// UnlinkResponse is the POST /v1.0/user/unlink response.
type UnlinkResponse struct {
	RequestID string `json:"request_id"`
}
