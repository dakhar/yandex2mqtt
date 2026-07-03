package device

// Definition is a device description for the get-devices response.
type Definition struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	Room         string          `json:"room"`
	Type         string          `json:"type"`
	CustomData   map[string]any  `json:"custom_data,omitempty"`
	Capabilities []CapabilityDef `json:"capabilities"`
	Properties   []CapabilityDef `json:"properties"`
}

// CapabilityDef is a capability/property declaration (shape shared by both).
type CapabilityDef struct {
	Type        string         `json:"type"`
	Retrievable bool           `json:"retrievable"`
	Reportable  bool           `json:"reportable"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// QueryResult is one device entry in the query response. When ErrorCode is set,
// Capabilities/Properties are ignored by Yandex.
type QueryResult struct {
	ID           string     `json:"id"`
	Capabilities []CapState `json:"capabilities,omitempty"`
	Properties   []CapState `json:"properties,omitempty"`
	ErrorCode    string     `json:"error_code,omitempty"`
	ErrorMessage string     `json:"error_message,omitempty"`
}

// CapState is a typed current state for query responses.
type CapState struct {
	Type  string `json:"type"`
	State *State `json:"state,omitempty"`
}

// ActionCapResult is one capability result in the action response.
type ActionCapResult struct {
	Type  string      `json:"type"`
	State ActionState `json:"state"`
}

// ActionState carries the per-instance action result.
type ActionState struct {
	Instance     string       `json:"instance"`
	ActionResult ActionResult `json:"action_result"`
}

// ActionResult reports the outcome of a single capability action.
type ActionResult struct {
	Status       string `json:"status"` // DONE | ERROR
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}
