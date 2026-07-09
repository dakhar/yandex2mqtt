package openhab

import (
	"strings"

	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
)

// This file maps openHAB's semantic model (Point role + Property + Equipment
// tags, see semantic_tags reference) onto Yandex capabilities/properties. Item
// type is used only as a fallback when semantic tags are absent.

// pointRole classifies a Point tag: "control" (writable), "event"/"readonly"
// (sensor), or "" when no point tag is present.
func pointRole(tags []string) string {
	for _, t := range tags {
		switch t {
		case "Setpoint", "Control", "Switch":
			return "control"
		case "Alarm":
			return "event"
		case "Measurement", "Status", "Calculation", "Forecast":
			return "readonly"
		}
	}
	return ""
}

// propertyTag returns the first recognized semantic Property tag.
func propertyTag(tags []string) string {
	known := map[string]bool{
		"Temperature": true, "Humidity": true, "Moisture": true, "Pressure": true,
		"Power": true, "Energy": true, "Current": true, "Voltage": true, "Frequency": true,
		"Brightness": true, "Color": true, "ColorTemperature": true, "SoundVolume": true,
		"Speed": true, "Motion": true, "Presence": true, "Opening": true, "OpenState": true,
		"OpenLevel": true, "Smoke": true, "Water": true, "Gas": true, "Vibration": true,
		"Illuminance": true, "CO2": true, "CO": true, "VOC": true, "ParticulateMatter": true,
		"LowBattery": true, "StateOfCharge": true, "Channel": true, "Position": true,
		"Tilt": true, "Mode": true, "MediaControl": true, "Light": true, "Tampered": true,
	}
	for _, t := range tags {
		if known[t] {
			return t
		}
	}
	return ""
}

// isSemanticPoint reports whether openHAB classifies the item as a Point (rather
// than Equipment) via its semantics metadata — authoritative over tag guessing.
func isSemanticPoint(it ohItem) bool {
	return strings.HasPrefix(it.Meta["semantics"].Value, "Point_")
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

// locationTags are the openHAB Location semantic tag leaf names (a group with
// one of these is a Room/place, not a device).
var locationTags = map[string]bool{
	"Indoor": true, "Apartment": true, "Building": true, "Garage": true, "House": true,
	"Shed": true, "SummerHouse": true, "Corridor": true, "Floor": true, "Attic": true,
	"Basement": true, "FirstFloor": true, "GroundFloor": true, "SecondFloor": true,
	"ThirdFloor": true, "Room": true, "Bathroom": true, "Bedroom": true, "BoilerRoom": true,
	"Cellar": true, "DiningRoom": true, "Entry": true, "FamilyRoom": true, "GuestRoom": true,
	"Kitchen": true, "LaundryRoom": true, "LivingRoom": true, "Office": true, "Veranda": true,
	"Outdoor": true, "Carport": true, "Driveway": true, "Garden": true, "Patio": true,
	"Porch": true, "Terrace": true,
}

// isLocation reports whether an item's tags mark it as a Location (Room).
func isLocation(tags []string) bool {
	for _, t := range tags {
		if locationTags[t] {
			return true
		}
	}
	return false
}

// featuresForItem maps an item to its Yandex features + a suggested standalone
// device type. ctxType is the containing Equipment's Yandex type ("" for a
// standalone item), used to disambiguate under-tagged analog points (e.g. a
// bare Setpoint Dimmer is a volume inside a media device, brightness inside a
// light). Empty result = unsupported item.
func featuresForItem(it ohItem, ctxType string) ([]feature, string) {
	// yahome metadata is an explicit override: it wins over tag inference.
	if f, dt, ok := yahomeFeatures(it); ok {
		return f, dt
	}
	role := pointRole(it.Tags)
	prop := propertyTag(it.Tags)
	base, _, _ := strings.Cut(it.Type, ":")
	// A Group used as a Point (Group:Switch/Dimmer/Color aggregating several
	// items) is treated by its base groupType.
	if base == "Group" && it.GroupType != "" {
		base, _, _ = strings.Cut(it.GroupType, ":")
	}

	// Sensor-event points (boolean-ish items) — a Motion "Switch" is an event,
	// not an on_off. Preempts the raw item type.
	if (base == "Switch" || base == "Contact") && role != "control" && prop != "" {
		if f, dt, ok := eventFeature(prop, it.Name); ok {
			return []feature{f}, dt
		}
	}

	switch base {
	case "Switch":
		// A light on a non-light appliance (e.g. a range-hood lamp) becomes a
		// backlight toggle, so it doesn't collide with the appliance's own on_off.
		if prop == "Light" && ctxType != "" && !strings.HasPrefix(ctxType, "devices.types.light") {
			return []feature{capFeat("backlight", capToggle("backlight"), it.Name)}, ctxType
		}
		return []feature{capFeat("on", capOnOff(), it.Name)}, deviceTypeFor(it.Tags, "devices.types.switch")
	case "String":
		// A camera's HLS-URL item (the IpCamera binding's canonical Cam<Name>HlsUrl,
		// holding an ipcamera/.../ipcamera.m3u8 path) -> a video_stream camera. The
		// connector resolves the relative path to an absolute URL at runtime.
		if isCameraHLSItem(it) {
			return []feature{capFeat("get_stream", capVideoStream(), it.Name)}, "devices.types.camera"
		}
		// The IpCamera binding's sibling Cam<Name>MjpegUrl -> an MJPEG video_stream
		// (lower latency than HLS, no audio); the connector resolves the relative
		// path the same way.
		if isCameraMJPEGItem(it) {
			return []feature{capFeat("get_stream", capVideoStream("mjpeg"), it.Name)}, "devices.types.camera"
		}
		// A String speed setpoint (options like off/low/medium/high) -> fan_speed.
		if hasTag(it.Tags, "Speed") {
			return []feature{stringSpeed(it)}, "devices.types.ventilation.fan"
		}
	case "Dimmer":
		return dimmerFeatures(it, ctxType)
	case "Color":
		return []feature{
			capFeat("on", capOnOff(), it.Name),
			capFeat("brightness", capBrightness(), it.Name),
			capFeat("hsv", capColorHSV(), it.Name),
		}, "devices.types.light"
	case "Rollershutter":
		// on_off drives open/close; a Rollershutter expects UP/DOWN, not ON/OFF.
		onoff := capFeat("on", capOnOff(), it.Name)
		onoff.mapY = []any{true, false}
		onoff.mapO = []any{"UP", "DOWN"}
		// openHAB Rollershutter counts 0% = open, so invert vs Yandex (100% = open).
		openCap := capRange("open", "unit.percent", 0, 100, 1)
		openCap.Invert = true
		return []feature{
			capFeat("open", openCap, it.Name),
			onoff,
		}, "devices.types.openable.curtain"
	case "Contact":
		return []feature{propFeat("open", propEvent("open", "opened", "closed"), it.Name)}, "devices.types.sensor.open"
	case "Number":
		return numberFeature(it, role, prop, ctxType)
	}
	return nil, ""
}

// dimmerFeatures maps a Dimmer (an analog 0-100 control) using its Property tag
// or, when untagged, the containing Equipment's type.
func dimmerFeatures(it ohItem, ctxType string) ([]feature, string) {
	dimLight := func() ([]feature, string) {
		return []feature{capFeat("on", capOnOff(), it.Name), capFeat("brightness", capBrightness(), it.Name)},
			deviceTypeFor(it.Tags, "devices.types.light")
	}
	volume := func() ([]feature, string) {
		return []feature{capFeat("volume", capRange("volume", "unit.percent", 0, 100, 1), it.Name)}, "devices.types.media_device"
	}
	tempSet := func(dt string) ([]feature, string) {
		min, max, prec := rangeParams(it, 10, 30, 0.5)
		return []feature{capFeat("temperature", capRange("temperature", "unit.temperature.celsius", min, max, prec), it.Name)}, dt
	}
	colorTemp := func() ([]feature, string) {
		return []feature{
			capFeat("on", capOnOff(), it.Name),
			capFeat("temperature_k", capColorTempK(2700, 6500), it.Name),
		}, deviceTypeFor(it.Tags, "devices.types.light")
	}
	position := func(dt string) ([]feature, string) {
		return []feature{capFeat("open", capRange("open", "unit.percent", 0, 100, 1), it.Name), capFeat("on", capOnOff(), it.Name)}, dt
	}

	// Property tag is the strongest signal.
	switch propertyTag(it.Tags) {
	case "SoundVolume":
		return volume()
	case "Brightness":
		return dimLight()
	case "Speed":
		return []feature{speedFeature(it)}, "devices.types.ventilation.fan"
	case "ColorTemperature":
		return colorTemp()
	case "Temperature":
		return tempSet("devices.types.thermostat")
	case "OpenLevel", "Position", "Opening":
		return position("devices.types.openable.curtain")
	}
	// Under-tagged: disambiguate by the containing equipment.
	switch {
	case isMediaType(ctxType):
		return volume()
	case ctxType == "devices.types.thermostat" || ctxType == "devices.types.thermostat.ac":
		return tempSet(ctxType)
	case strings.HasPrefix(ctxType, "devices.types.openable"):
		return position(ctxType)
	}
	// Default: a dimmable light.
	return dimLight()
}

func isMediaType(t string) bool {
	return strings.HasPrefix(t, "devices.types.media_device")
}

// eventFeature maps a sensor Property tag to a Yandex event property.
func eventFeature(prop, item string) (feature, string, bool) {
	switch prop {
	case "Motion", "Presence":
		return propFeat("motion", propEvent("motion", "detected", "not_detected"), item), "devices.types.sensor.motion", true
	case "Opening", "OpenState":
		return propFeat("open", propEvent("open", "opened", "closed"), item), "devices.types.sensor.open", true
	case "Smoke":
		return propFeat("smoke", propEvent("smoke", "detected", "not_detected", "high"), item), "devices.types.sensor.smoke", true
	case "Water":
		return propFeat("water_leak", propEvent("water_leak", "dry", "leak"), item), "devices.types.sensor.water_leak", true
	case "Vibration":
		return propFeat("vibration", propEvent("vibration", "tilt", "fall", "vibration"), item), "devices.types.sensor.vibration", true
	case "LowBattery":
		return propFeat("battery_level", propEvent("battery_level", "low", "normal"), item), "devices.types.sensor", true
	}
	return feature{}, "", false
}

// numberFeature maps a Number item to a float property (Measurement) or a
// controllable range (Setpoint), using its Property tag or dimension.
func numberFeature(it ohItem, role, prop, ctxType string) ([]feature, string) {
	_, dim, _ := strings.Cut(it.Type, ":")
	if prop == "" {
		prop = dim // "Number:Temperature" -> Temperature
	}
	setpoint := role == "control" || hasTag(it.Tags, "Setpoint")
	// An under-tagged Setpoint takes its meaning from the containing equipment.
	if setpoint && prop == "" {
		switch {
		case isMediaType(ctxType):
			return []feature{capFeat("volume", capRange("volume", "unit.percent", 0, 100, 1), it.Name)}, "devices.types.media_device"
		case ctxType == "devices.types.thermostat" || ctxType == "devices.types.thermostat.ac":
			return []feature{capFeat("temperature", capRange("temperature", "unit.temperature.celsius", 10, 30, 0.5), it.Name)}, ctxType
		}
	}

	f := func(feat feature, dtype string) ([]feature, string) { return []feature{feat}, dtype }
	fl := func(inst, unit, dtype string) ([]feature, string) {
		return f(propFeat(inst, propFloat(inst, unit), it.Name), dtype)
	}

	switch prop {
	case "Temperature":
		if setpoint {
			min, max, prec := rangeParams(it, 10, 30, 0.5)
			return f(capFeat("temperature", capRange("temperature", "unit.temperature.celsius", min, max, prec), it.Name), "devices.types.thermostat")
		}
		return fl("temperature", "unit.temperature.celsius", "devices.types.sensor.climate")
	case "Humidity", "Moisture":
		return fl("humidity", "unit.percent", "devices.types.sensor.climate")
	case "Pressure":
		return fl("pressure", "unit.pressure.mmhg", "devices.types.sensor.climate")
	case "Power":
		return fl("power", "unit.watt", "devices.types.sensor")
	case "Energy":
		return fl("electricity_meter", "unit.kilowatt_hour", "devices.types.smart_meter.electricity")
	case "Current":
		return fl("amperage", "unit.ampere", "devices.types.sensor")
	case "Voltage":
		return fl("voltage", "unit.volt", "devices.types.sensor")
	case "Water":
		return fl("water_meter", "unit.cubic_meter", "devices.types.smart_meter")
	case "Gas":
		return fl("gas_meter", "unit.cubic_meter", "devices.types.smart_meter.gas")
	case "Illuminance":
		return fl("illumination", "unit.illumination.lux", "devices.types.sensor.illumination")
	case "CO2":
		return fl("co2_level", "unit.ppm", "devices.types.sensor.climate")
	case "VOC":
		return fl("tvoc", "unit.density.mcg_m3", "devices.types.sensor.climate")
	case "ParticulateMatter":
		return fl("pm2.5_density", "unit.density.mcg_m3", "devices.types.sensor.climate")
	case "StateOfCharge":
		return fl("battery_level", "unit.percent", "devices.types.sensor")
	case "SoundVolume":
		if setpoint {
			return f(capFeat("volume", capRange("volume", "unit.percent", 0, 100, 1), it.Name), "devices.types.media_device")
		}
	case "Brightness":
		if setpoint {
			return f(capFeat("brightness", capBrightness(), it.Name), "devices.types.light")
		}
	case "Speed":
		if setpoint {
			return []feature{speedFeature(it)}, "devices.types.ventilation.fan"
		}
	}
	return nil, ""
}

// isCameraHLSItem reports whether a String item is a camera's HLS-playlist URL,
// by the IpCamera binding's canonical Cam<Name>HlsUrl naming (case-insensitive).
// The sibling Cam<Name>ImageUrl (a JPG snapshot) is intentionally excluded — a
// video_stream needs the .m3u8 playlist, not a still.
func isCameraHLSItem(it ohItem) bool {
	return it.Type == "String" && strings.HasSuffix(strings.ToLower(it.Name), "hlsurl")
}

// isCameraMJPEGItem reports whether a String item is a camera's MJPEG-stream URL,
// by the IpCamera binding's canonical Cam<Name>MjpegUrl naming (case-insensitive).
func isCameraMJPEGItem(it ohItem) bool {
	return it.Type == "String" && strings.HasSuffix(strings.ToLower(it.Name), "mjpegurl")
}

// deviceTypeFor picks a device type from an item's own semantic tags.
func deviceTypeFor(tags []string, def string) string {
	if t := equipmentType(tags); t != "" {
		return t
	}
	return def
}

// equipmentType maps openHAB Equipment (and some point) tags to a Yandex device
// type. Covers the common subset of the semantic model.
func equipmentType(tags []string) string {
	for _, t := range tags {
		switch t {
		// lighting
		case "Chandelier":
			return "devices.types.light.ceiling"
		case "LightStrip", "LightStripe":
			return "devices.types.light.strip"
		case "LightSource", "Lightbulb", "Lamp", "Pendant",
			"Sconce", "Downlight", "FloodLight", "SpotLight", "TrackLight",
			"WallLight", "AccentLight", "Light":
			return "devices.types.light"
		// electrics
		case "PowerOutlet":
			return "devices.types.socket"
		case "WallSwitch":
			return "devices.types.switch"
		// climate / ventilation
		case "Fan", "CeilingFan":
			return "devices.types.ventilation.fan"
		case "ExhaustFan", "KitchenHood", "SmartVent", "HeatRecovery":
			return "devices.types.ventilation"
		case "Thermostat", "RadiatorControl", "HVAC", "Boiler", "WaterHeater", "FloorHeating", "HeatPump", "Furnace":
			return "devices.types.thermostat"
		case "AirConditioner":
			return "devices.types.thermostat.ac"
		case "Humidifier", "Dehumidifier":
			return "devices.types.humidifier"
		case "AirFilter":
			return "devices.types.purifier"
		// openables
		case "Blinds", "Drapes", "WindowCovering":
			return "devices.types.openable.curtain"
		case "Window", "Door", "FrontDoor", "BackDoor", "SideDoor", "InnerDoor", "CellarDoor", "GarageDoor", "Gate":
			return "devices.types.openable"
		case "Lock":
			return "devices.types.openable.door_lock"
		case "Valve":
			return "devices.types.openable.valve"
		// appliances
		case "CleaningRobot":
			return "devices.types.vacuum_cleaner"
		case "WashingMachine":
			return "devices.types.washing_machine"
		case "Dishwasher":
			return "devices.types.dishwasher"
		case "CoffeeMaker":
			return "devices.types.cooking.coffee_maker"
		case "Microwave", "Oven", "Cooktop", "Range", "Toaster", "AirFryer", "Fryer", "FoodProcessor", "Mixer", "IceMaker", "Refrigerator", "Freezer":
			return "devices.types.cooking"
		case "PetFeeder":
			return "devices.types.pet_feeder"
		// media
		case "Television":
			return "devices.types.media_device.tv"
		case "Receiver":
			return "devices.types.media_device.receiver"
		case "Speaker", "MediaPlayer", "AudioVisual", "Display", "VoiceAssistant":
			return "devices.types.media_device"
		case "Camera", "Doorbell":
			return "devices.types.camera"
		// sensors
		case "MotionDetector", "OccupancySensor":
			return "devices.types.sensor.motion"
		case "ContactSensor":
			return "devices.types.sensor.open"
		case "SmokeDetector", "FireDetector", "FlameDetector", "HeatDetector":
			return "devices.types.sensor.smoke"
		case "LeakSensor":
			return "devices.types.sensor.water_leak"
		case "IlluminanceSensor":
			return "devices.types.sensor.illumination"
		case "VibrationSensor", "GlassBreakDetector":
			return "devices.types.sensor.vibration"
		case "COSensor", "CO2Sensor", "AirQualitySensor", "GasMeter":
			return "devices.types.sensor.gas"
		case "TemperatureSensor", "HumiditySensor", "WeatherStation":
			return "devices.types.sensor.climate"
		case "ElectricMeter":
			return "devices.types.smart_meter.electricity"
		case "WaterMeter":
			return "devices.types.smart_meter"
		case "Button", "Keypad", "Dial", "Slider", "RemoteControl":
			return "devices.types.sensor.button"
		case "Sensor":
			return "devices.types.sensor"
		}
	}
	return ""
}

// --- capability/property constructors ---

func capOnOff() config.Capability {
	return config.Capability{Type: "devices.capabilities.on_off", Retrievable: true, Reportable: true}
}

func capBrightness() config.Capability {
	return capRange("brightness", "unit.percent", 0, 100, 1)
}

func capRange(instance, unit string, min, max, precision float64) config.Capability {
	return config.Capability{
		Type: "devices.capabilities.range", Retrievable: true, Reportable: true,
		Parameters: map[string]any{
			"instance": instance, "unit": unit,
			"range": map[string]any{"min": min, "max": max, "precision": precision},
		},
	}
}

// capActType extracts a capability's act-type ("devices.capabilities.mode" ->
// "mode") for value-mapping grouping.
func capActType(capType string) string {
	p := strings.Split(capType, ".")
	if len(p) >= 3 {
		return p[2]
	}
	return ""
}

// rangeParams derives a range capability's min/max/precision from an item's
// stateDescription, falling back to the supplied defaults. Used for real-unit
// setpoints (temperature); percent instances keep 0-100.
func rangeParams(it ohItem, dMin, dMax, dPrec float64) (float64, float64, float64) {
	min, max, prec := dMin, dMax, dPrec
	if sd := it.StateDesc; sd != nil {
		if sd.Minimum != nil {
			min = *sd.Minimum
		}
		if sd.Maximum != nil {
			max = *sd.Maximum
		}
		if sd.Step != nil && *sd.Step > 0 {
			prec = *sd.Step
		}
	}
	return min, max, prec
}

// speedInstance picks the Yandex mode instance for a Speed property. A generic
// Speed is "work speed"; it is only "fan speed" when the item name mentions a
// fan (e.g. a real ventilation fan), so a vacuum's suction stays work_speed.
func speedInstance(name string) string {
	if strings.Contains(strings.ToLower(name), "fan") {
		return "fan_speed"
	}
	return "work_speed"
}

// speedLadder is the ascending intensity vocabulary valid for a speed instance
// (values must be in Yandex's per-instance allowed set).
func speedLadder(instance string) []string {
	if instance == "fan_speed" {
		return []string{"low", "medium", "high", "turbo"}
	}
	return []string{"min", "slow", "medium", "fast", "max", "turbo"} // work_speed
}

// isOffValue reports a device value meaning "off" (power is the on_off, not a speed).
func isOffValue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "0", "none", "stop":
		return true
	}
	return false
}

// stringSpeed builds a speed mode capability from a String item's enumerated
// options (off-like values dropped — power is the on_off), mapped onto the
// instance's intensity ladder with a value mapping of the device's own tokens.
func stringSpeed(it ohItem) feature {
	inst := speedInstance(it.Name)
	var opts []any
	if it.StateDesc != nil {
		for _, o := range it.StateDesc.Options {
			if !isOffValue(o.Value) {
				opts = append(opts, o.Value)
			}
		}
	}
	if len(opts) == 0 {
		return capFeat(inst, capMode(inst, device.RecommendedModes(inst)), it.Name)
	}
	modes, mapY, mapO := ladderMapping(opts, speedLadder(inst))
	f := capFeat(inst, capMode(inst, modes), it.Name)
	f.mapY, f.mapO = mapY, mapO
	return f
}

// speedFeature builds a speed mode capability from a numeric item, deriving the
// mode set and value mapping (mode <-> device number) from its stateDescription.
func speedFeature(it ohItem) feature {
	inst := speedInstance(it.Name)
	modes, mapY, mapO := speedModes(it.StateDesc, inst)
	f := capFeat(inst, capMode(inst, modes), it.Name)
	f.mapY, f.mapO = mapY, mapO
	return f
}

// speedModes maps a numeric speed range onto the instance's intensity ladder with
// a value mapping. A 0 minimum is treated as "off" (power via on_off) and skipped.
// With no usable range it falls back to the recommended modes and no mapping.
func speedModes(sd *ohStateDesc, inst string) (modes []string, mapY, mapO []any) {
	ladder := speedLadder(inst)
	if sd == nil || sd.Minimum == nil || sd.Maximum == nil {
		return device.RecommendedModes(inst), nil, nil
	}
	step := 1.0
	if sd.Step != nil && *sd.Step > 0 {
		step = *sd.Step
	}
	min := *sd.Minimum
	if min == 0 {
		min = step // 0 == off, controlled via on_off
	}
	var values []any
	for v := min; v <= *sd.Maximum+1e-9; v += step {
		values = append(values, numClean(v))
	}
	if len(values) < 1 {
		return device.RecommendedModes(inst), nil, nil
	}
	return ladderMapping(values, ladder)
}

// ladderMapping assigns each device value a mode from the intensity ladder and
// builds the value mapping. When there are more device values than ladder rungs,
// values are distributed across the ladder (collapsing neighbours) rather than
// dropped — so a >4-preset fan_speed keeps a full mapping instead of falling
// back. modes are the distinct ladder tokens used, in order.
func ladderMapping(devValues []any, ladder []string) (modes []string, mapY, mapO []any) {
	n := len(devValues)
	seen := map[string]bool{}
	for i, dev := range devValues {
		idx := i
		if n > len(ladder) {
			idx = i * len(ladder) / n // spread N values across the ladder
		}
		if idx >= len(ladder) {
			idx = len(ladder) - 1
		}
		tok := ladder[idx]
		if !seen[tok] {
			seen[tok] = true
			modes = append(modes, tok)
		}
		mapY = append(mapY, tok)
		mapO = append(mapO, dev)
	}
	return modes, mapY, mapO
}

// numClean renders a whole float as an int (1, not 1.0) for clean value mapping.
func numClean(v float64) any {
	if v == float64(int64(v)) {
		return int64(v)
	}
	return v
}

func capMode(instance string, modes []string) config.Capability {
	ms := make([]any, len(modes))
	for i, m := range modes {
		ms[i] = map[string]any{"value": m}
	}
	return config.Capability{
		Type: "devices.capabilities.mode", Retrievable: true, Reportable: true,
		Parameters: map[string]any{"instance": instance, "modes": ms},
	}
}

// capVideoStream builds a video_stream capability declaring the given protocols
// (defaulting to "hls"). Yandex's player negotiates the returned stream against
// these; a camera typically declares the single protocol its URL serves.
func capVideoStream(protocols ...string) config.Capability {
	ps := make([]any, 0, len(protocols))
	for _, p := range protocols {
		ps = append(ps, p)
	}
	if len(ps) == 0 {
		ps = []any{"hls"}
	}
	return config.Capability{
		Type: "devices.capabilities.video_stream", Retrievable: false, Reportable: false,
		Parameters: map[string]any{"protocols": ps},
	}
}

func capToggle(instance string) config.Capability {
	return config.Capability{
		Type: "devices.capabilities.toggle", Retrievable: true, Reportable: true,
		Parameters: map[string]any{"instance": instance},
	}
}

func capColorTempK(min, max int) config.Capability {
	return config.Capability{
		Type: "devices.capabilities.color_setting", Retrievable: true, Reportable: true,
		Parameters: map[string]any{"temperature_k": map[string]any{"min": min, "max": max}},
	}
}

func capColorHSV() config.Capability {
	return config.Capability{
		Type: "devices.capabilities.color_setting", Retrievable: true, Reportable: true,
		Parameters: map[string]any{"color_model": "hsv"},
	}
}

func propFloat(instance, unit string) config.Property {
	return config.Property{
		Type: "devices.properties.float", Retrievable: true, Reportable: true,
		Parameters: map[string]any{"instance": instance, "unit": unit},
	}
}

func propEvent(instance string, values ...string) config.Property {
	events := make([]any, len(values))
	for i, v := range values {
		events[i] = map[string]any{"value": v}
	}
	return config.Property{
		Type: "devices.properties.event", Retrievable: true, Reportable: true,
		Parameters: map[string]any{"instance": instance, "events": events},
	}
}

func capFeat(instance string, c config.Capability, item string) feature {
	return feature{kind: "cap", instance: instance, cap: c, item: item}
}
func propFeat(instance string, p config.Property, item string) feature {
	return feature{kind: "prop", instance: instance, prop: p, item: item}
}

func itemLabel(it ohItem) string {
	if it.Label != "" {
		return it.Label
	}
	return it.Name
}
