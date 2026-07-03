package openhab

import (
	"strings"

	"github.com/dakhar/yandex2mqtt/internal/config"
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

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

// featuresForItem maps an item to its Yandex features + a suggested standalone
// device type. Empty result = unsupported item.
func featuresForItem(it ohItem) ([]feature, string) {
	role := pointRole(it.Tags)
	prop := propertyTag(it.Tags)
	base, _, _ := strings.Cut(it.Type, ":")

	// Sensor-event points (boolean-ish items) — a Motion "Switch" is an event,
	// not an on_off. Preempts the raw item type.
	if (base == "Switch" || base == "Contact") && role != "control" && prop != "" {
		if f, dt, ok := eventFeature(prop, it.Name); ok {
			return []feature{f}, dt
		}
	}

	switch base {
	case "Switch":
		return []feature{capFeat("on", capOnOff(), it.Name)}, deviceTypeFor(it.Tags, "devices.types.switch")
	case "Dimmer":
		return []feature{
			capFeat("on", capOnOff(), it.Name),
			capFeat("brightness", capBrightness(), it.Name),
		}, deviceTypeFor(it.Tags, "devices.types.light")
	case "Color":
		return []feature{
			capFeat("on", capOnOff(), it.Name),
			capFeat("brightness", capBrightness(), it.Name),
			capFeat("hsv", capColorHSV(), it.Name),
		}, "devices.types.light"
	case "Rollershutter":
		return []feature{
			capFeat("open", capRange("open", "unit.percent", 0, 100, 1), it.Name),
			capFeat("on", capOnOff(), it.Name),
		}, "devices.types.openable.curtain"
	case "Contact":
		return []feature{propFeat("open", propEvent("open", "opened", "closed"), it.Name)}, "devices.types.sensor.open"
	case "Number":
		return numberFeature(it, role, prop)
	}
	return nil, ""
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
func numberFeature(it ohItem, role, prop string) ([]feature, string) {
	_, dim, _ := strings.Cut(it.Type, ":")
	if prop == "" {
		prop = dim // "Number:Temperature" -> Temperature
	}
	setpoint := role == "control" || hasTag(it.Tags, "Setpoint")

	f := func(feat feature, dtype string) ([]feature, string) { return []feature{feat}, dtype }
	fl := func(inst, unit, dtype string) ([]feature, string) {
		return f(propFeat(inst, propFloat(inst, unit), it.Name), dtype)
	}

	switch prop {
	case "Temperature":
		if setpoint {
			return f(capFeat("temperature", capRange("temperature", "unit.temperature.celsius", 10, 30, 0.5), it.Name), "devices.types.thermostat")
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
	}
	return nil, ""
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
		case "LightSource", "Lightbulb", "Lamp", "LightStrip", "LightStripe", "Pendant",
			"Sconce", "Chandelier", "Downlight", "FloodLight", "SpotLight", "TrackLight",
			"WallLight", "AccentLight", "Light", "Ceiling":
			return "devices.types.light"
		// electrics
		case "PowerOutlet", "WallOutlet":
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
		case "Speaker", "MediaPlayer", "AudioVisual", "Display":
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
