package device

import (
	"log/slog"
	"math"
	"strconv"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

// PublishFunc publishes a payload to an MQTT topic. Injected so the domain
// model stays decoupled from the MQTT client (no globals).
type PublishFunc func(topic, payload string)

// State is a Yandex capability/property state: an instance and its value.
type State struct {
	Instance string `json:"instance"`
	Value    any    `json:"value"`
}

// Device is a single smart-home device with mutable current state.
type Device struct {
	ID           string
	Name         string
	Description  string
	Room         string
	Type         string
	Transport    string // "mqtt" (default) | "openhab"
	AllowedUsers []string

	mqtt         config.MQTTMapping
	valueMapping []config.ValueMapping

	capabilities []*capState
	properties   []*propState

	// Transport-agnostic wiring (built from MQTT topics or openHAB items).
	cmdTarget     map[string]string // instance -> command target (topic/item)
	stateBindings []StateBinding

	publish PublishFunc
	log     *slog.Logger
}

type capState struct {
	Type        string
	Retrievable bool
	Reportable  bool
	Parameters  map[string]any
	cur         *State
}

type propState struct {
	Type        string
	Retrievable bool
	Reportable  bool
	Parameters  map[string]any
	cur         *State
}

// New builds a Device from its config definition. publish may be nil (e.g. in
// tests that only read state); log defaults to a discarding logger.
func New(c config.Device, publish PublishFunc, log *slog.Logger) *Device {
	if log == nil {
		log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	transport := c.Transport
	if transport == "" {
		transport = "mqtt"
	}
	d := &Device{
		ID:           c.ID,
		Name:         c.Name,
		Description:  c.Description,
		Room:         c.Room,
		Type:         c.Type,
		Transport:    transport,
		AllowedUsers: c.AllowedUsers,
		mqtt:         c.MQTT,
		valueMapping: c.ValueMapping,
		publish:      publish,
		log:          log,
	}
	if len(d.AllowedUsers) == 0 {
		d.AllowedUsers = []string{"1"}
	}
	for _, c := range c.Capabilities {
		d.capabilities = append(d.capabilities, &capState{
			Type:        c.Type,
			Retrievable: c.Retrievable,
			Reportable:  c.Reportable,
			Parameters:  c.Parameters,
			cur:         initState(c.Type, c.Parameters),
		})
	}
	for _, p := range c.Properties {
		d.properties = append(d.properties, &propState{
			Type:        p.Type,
			Retrievable: p.Retrievable,
			Reportable:  p.Reportable,
			Parameters:  p.Parameters,
			cur:         initState(p.Type, p.Parameters),
		})
	}
	d.buildBindings(c)
	return d
}

// initState builds the initial state for a capability/property. Port of
// initState in device.js, collapsed to a single {instance,value} (the original
// color_setting composite was only ever reported via its message_state).
func initState(typ string, params map[string]any) *State {
	inst, _ := params["instance"].(string)
	switch actTypeOf(typ) {
	case "float":
		return &State{Instance: inst, Value: 0.0}
	case "on_off":
		return &State{Instance: "on", Value: false}
	case "mode":
		return &State{Instance: inst, Value: firstMode(params)}
	case "range":
		return &State{Instance: inst, Value: rangeMin(params)}
	case "toggle":
		return &State{Instance: inst, Value: false}
	case "event":
		return &State{Instance: inst, Value: nil}
	case "color_setting":
		switch {
		case hasKey(params, "temperature_k"):
			min, _ := tempRange(params)
			return &State{Instance: "temperature_k", Value: min}
		case params["color_model"] == "hsv":
			return &State{Instance: "hsv", Value: HSV{}}
		case params["color_model"] == "rgb", hasKey(params, "rgb"):
			return &State{Instance: "rgb", Value: 0.0}
		case hasKey(params, "color_scene"):
			return &State{Instance: "scene", Value: firstScene(params)}
		default:
			return nil
		}
	default:
		return nil
	}
}

// Definition returns the device description for the get-devices response.
func (d *Device) Definition() Definition {
	def := Definition{
		ID:          d.ID,
		Name:        d.Name,
		Description: d.Description,
		Room:        d.Room,
		Type:        d.Type,
	}
	for _, c := range d.capabilities {
		def.Capabilities = append(def.Capabilities, CapabilityDef{
			Type:        c.Type,
			Retrievable: c.Retrievable,
			Reportable:  c.Reportable,
			Parameters:  c.Parameters,
		})
	}
	for _, p := range d.properties {
		def.Properties = append(def.Properties, CapabilityDef{
			Type:        p.Type,
			Retrievable: p.Retrievable,
			Reportable:  p.Reportable,
			Parameters:  p.Parameters,
		})
	}
	return def
}

// QueryState returns the current retrievable state for the query response.
func (d *Device) QueryState() QueryResult {
	r := QueryResult{ID: d.ID}
	for _, c := range d.capabilities {
		if c.Retrievable && c.cur != nil {
			r.Capabilities = append(r.Capabilities, CapState{Type: c.Type, State: c.cur})
		}
	}
	for _, p := range d.properties {
		if p.Retrievable && p.cur != nil {
			r.Properties = append(r.Properties, CapState{Type: p.Type, State: p.cur})
		}
	}
	return r
}

// SetCapabilityState applies a Yandex action: it maps the value, updates the
// current state, publishes to MQTT, and returns the per-capability result.
// Port of setCapabilityState in device.js.
func (d *Device) SetCapabilityState(val any, capType, instance string, relative bool) ActionCapResult {
	actType := actTypeOf(capType)
	value := d.mapValue(val, actType, instance, true)

	cap := d.findCap(capType, instance)
	if cap == nil {
		return actionErr(capType, instance, "INVALID_ACTION", "capability not found")
	}
	target := d.cmdTarget[instance]
	if target == "" {
		return actionErr(capType, instance, "INVALID_ACTION", "no command binding for instance")
	}

	cap.cur = &State{Instance: instance, Value: value}

	var message string
	if instance == "temperature_k" {
		min, max := tempRange(cap.Parameters)
		divider := (max - min) / 100
		message = strconv.Itoa(int(math.Floor((toFloatOr(value, 0) - min) / divider)))
	} else if relative {
		message = relativeMessage(value)
	} else {
		message = num2str(value)
	}

	if d.publish != nil {
		d.publish(target, message)
	}
	return ActionCapResult{
		Type:  capType,
		State: ActionState{Instance: instance, ActionResult: ActionResult{Status: "DONE"}},
	}
}

// UpdateFromMQTT applies an incoming MQTT message to the matching capability or
// property. Port of updateState in device.js.
func (d *Device) UpdateFromMQTT(val string, instance string, isProp bool) {
	colorInstances := map[string]bool{
		"temperature_k": true, "hsv": true, "rgb": true, "scene": true,
		"color_model": true, "color_scene": true,
	}

	var (
		typ    string
		params map[string]any
		set    func(*State)
	)
	switch {
	case isProp:
		p := d.findPropByInstance(instance)
		if p == nil {
			d.log.Warn("mqtt update: unknown property instance", "device", d.ID, "instance", instance)
			return
		}
		typ, params = p.Type, p.Parameters
		set = func(s *State) { p.cur = s }
	case colorInstances[instance]:
		c := d.findCapByType("devices.capabilities.color_setting")
		if c == nil {
			d.log.Warn("mqtt update: no color_setting capability", "device", d.ID, "instance", instance)
			return
		}
		typ, params = c.Type, c.Parameters
		set = func(s *State) { c.cur = s }
	default:
		c := d.findCapByInstance(instance)
		if c == nil {
			d.log.Warn("mqtt update: unknown capability instance", "device", d.ID, "instance", instance)
			return
		}
		typ, params = c.Type, c.Parameters
		set = func(s *State) { c.cur = s }
	}

	actType := actTypeOf(typ)
	mapped := d.mapValue(val, actType, instance, false)
	set(&State{Instance: instance, Value: convertToYandexValue(mapped, actType, instance, params)})
}

// --- lookups ---

func (d *Device) findCap(capType, instance string) *capState {
	if actTypeOf(capType) == "color_setting" {
		return d.findCapByType(capType)
	}
	for _, c := range d.capabilities {
		if c.Type == capType && c.cur != nil && c.cur.Instance == instance {
			return c
		}
	}
	return nil
}

func (d *Device) findCapByType(capType string) *capState {
	for _, c := range d.capabilities {
		if c.Type == capType {
			return c
		}
	}
	return nil
}

func (d *Device) findCapByInstance(instance string) *capState {
	for _, c := range d.capabilities {
		if c.cur != nil && c.cur.Instance == instance {
			return c
		}
	}
	return nil
}

func (d *Device) findPropByInstance(instance string) *propState {
	for _, p := range d.properties {
		if p.cur != nil && p.cur.Instance == instance {
			return p
		}
	}
	return nil
}

// SetPublisher wires the MQTT publisher into the device after construction
// (the bridge needs the devices before it can offer a publisher).
func (d *Device) SetPublisher(p PublishFunc) { d.publish = p }

// StateBinding ties a capability/property instance to its external state source
// (an MQTT state topic or an openHAB item), for connectors to subscribe to.
type StateBinding struct {
	Instance string
	Source   string
	IsProp   bool
}

// StateBindings returns the device's inbound state sources (transport-agnostic).
func (d *Device) StateBindings() []StateBinding { return d.stateBindings }

// buildBindings populates the transport-agnostic command targets and state
// bindings from the device's MQTT topics or openHAB items.
func (d *Device) buildBindings(c config.Device) {
	d.cmdTarget = map[string]string{}
	if d.Transport == "openhab" {
		for _, b := range c.OpenHAB {
			if b.Kind == "prop" {
				d.stateBindings = append(d.stateBindings, StateBinding{Instance: b.Instance, Source: b.Item, IsProp: true})
				continue
			}
			d.cmdTarget[b.Instance] = b.Item
			d.stateBindings = append(d.stateBindings, StateBinding{Instance: b.Instance, Source: b.Item, IsProp: false})
		}
		return
	}
	// mqtt (default)
	for _, t := range c.MQTT.Capabilities {
		if t.Set != "" {
			d.cmdTarget[t.Instance] = t.Set
		}
		if t.State != "" {
			d.stateBindings = append(d.stateBindings, StateBinding{Instance: t.Instance, Source: t.State, IsProp: false})
		}
	}
	for _, t := range c.MQTT.Properties {
		if t.State != "" {
			d.stateBindings = append(d.stateBindings, StateBinding{Instance: t.Instance, Source: t.State, IsProp: true})
		}
	}
}

// --- helpers ---

func relativeMessage(value any) string {
	if f, err := toFloat(value); err == nil {
		if f < 0 {
			return num2str(value)
		}
		return "+" + num2str(value)
	}
	return num2str(value)
}

// num2str renders a value the way JS template interpolation would (numbers
// without trailing ".0", bools as true/false, strings verbatim).
func num2str(value any) string {
	switch v := value.(type) {
	case float64:
		return num(v)
	case float32:
		return num(float64(v))
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case string:
		return v
	default:
		return num(toFloatOr(value, 0))
	}
}

func actionErr(capType, instance, code, msg string) ActionCapResult {
	return ActionCapResult{
		Type: capType,
		State: ActionState{
			Instance:     instance,
			ActionResult: ActionResult{Status: "ERROR", ErrorCode: code, ErrorMessage: msg},
		},
	}
}

func firstMode(params map[string]any) any {
	modes, ok := params["modes"].([]any)
	if !ok || len(modes) == 0 {
		return nil
	}
	m, ok := modes[0].(map[string]any)
	if !ok {
		return nil
	}
	return m["value"]
}

func firstScene(params map[string]any) any {
	cs, ok := params["color_scene"].(map[string]any)
	if !ok {
		return nil
	}
	scenes, ok := cs["scenes"].([]any)
	if !ok || len(scenes) == 0 {
		return nil
	}
	if m, ok := scenes[0].(map[string]any); ok {
		return m["id"]
	}
	return nil
}

func rangeMin(params map[string]any) float64 {
	r, ok := params["range"].(map[string]any)
	if !ok {
		return 0
	}
	return toFloatOr(r["min"], 0)
}

func hasKey(m map[string]any, k string) bool {
	_, ok := m[k]
	return ok
}

// discard is an io.Writer that drops everything (for the default logger).
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
