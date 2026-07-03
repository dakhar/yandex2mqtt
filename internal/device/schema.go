package device

// Reference tables from the Yandex Smart Home docs
// (concepts/device-types, on_off, color_setting, mode, range, toggle, float,
// event). Used to validate the device catalog. Unknown *types* are fatal;
// unknown instances/units/values are warnings so newly-added Yandex values
// don't break an existing catalog.

type set map[string]struct{}

func newSet(items ...string) set {
	s := make(set, len(items))
	for _, i := range items {
		s[i] = struct{}{}
	}
	return s
}

func (s set) has(x string) bool { _, ok := s[x]; return ok }

var capabilityTypes = newSet(
	"devices.capabilities.on_off",
	"devices.capabilities.color_setting",
	"devices.capabilities.mode",
	"devices.capabilities.range",
	"devices.capabilities.toggle",
	"devices.capabilities.video_stream",
)

var propertyTypes = newSet(
	"devices.properties.float",
	"devices.properties.event",
)

var deviceTypes = newSet(
	"devices.types.sensor", "devices.types.sensor.button", "devices.types.sensor.climate",
	"devices.types.sensor.gas", "devices.types.sensor.illumination", "devices.types.sensor.motion",
	"devices.types.sensor.open", "devices.types.sensor.smoke", "devices.types.sensor.vibration",
	"devices.types.sensor.water_leak",
	"devices.types.smart_meter", "devices.types.smart_meter.cold_water",
	"devices.types.smart_meter.electricity", "devices.types.smart_meter.gas",
	"devices.types.smart_meter.heat", "devices.types.smart_meter.hot_water",
	"devices.types.camera", "devices.types.media_device", "devices.types.media_device.receiver",
	"devices.types.media_device.tv", "devices.types.media_device.tv_box",
	"devices.types.cooking", "devices.types.cooking.coffee_maker", "devices.types.cooking.kettle",
	"devices.types.cooking.multicooker", "devices.types.dishwasher",
	"devices.types.iron", "devices.types.vacuum_cleaner", "devices.types.washing_machine",
	"devices.types.pet_drinking_fountain", "devices.types.pet_feeder",
	"devices.types.humidifier", "devices.types.purifier", "devices.types.thermostat",
	"devices.types.thermostat.ac", "devices.types.ventilation", "devices.types.ventilation.fan",
	"devices.types.light", "devices.types.light.lamp", "devices.types.light.ceiling",
	"devices.types.light.strip", "devices.types.socket", "devices.types.switch",
	"devices.types.switch.relay",
	"devices.types.openable", "devices.types.openable.curtain", "devices.types.openable.door_lock",
	"devices.types.openable.valve",
	"devices.types.other",
)

var modeInstances = newSet(
	"cleanup_mode", "coffee_mode", "dishwashing", "fan_speed", "heat", "input_source",
	"program", "swing", "tea_mode", "thermostat", "ventilation_mode", "work_speed",
)

var modeValues = newSet(
	"wet_cleaning", "dry_cleaning", "mixed_cleaning", "auto", "eco", "smart", "turbo",
	"cool", "dry", "fan_only", "heat", "preheat", "high", "low", "medium", "max", "min",
	"fast", "slow", "express", "normal", "quiet", "horizontal", "stationary", "vertical",
	"supply_air", "extraction_air", "one", "two", "three", "four", "five", "six", "seven",
	"eight", "nine", "ten", "americano", "cappuccino", "double_espresso", "espresso", "latte",
	"black_tea", "flower_tea", "green_tea", "herbal_tea", "oolong_tea", "puerh_tea", "red_tea",
	"white_tea", "glass", "intensive", "pre_rinse", "aspic", "baby_food", "baking", "bread",
	"boiling", "cereals", "cheesecake", "deep_fryer", "dessert", "fowl", "frying", "macaroni",
	"milk_porridge", "multicooker", "pasta", "pilaf", "pizza", "sauce", "slow_cook", "soup",
	"steam", "stewing", "vacuum", "yogurt",
)

var toggleInstances = newSet(
	"backlight", "controls_locked", "ionization", "keep_warm", "mute", "oscillation", "pause",
)

var colorScenes = newSet(
	"alarm", "alice", "candle", "dinner", "fantasy", "garland", "jungle", "movie", "neon",
	"night", "ocean", "party", "reading", "rest", "romance", "siren",
)

// rangeUnits maps a range instance to its allowed units (empty set = unitless allowed).
var rangeUnits = map[string]set{
	"brightness":  newSet("unit.percent"),
	"channel":     newSet(),
	"humidity":    newSet("unit.percent"),
	"open":        newSet("unit.percent"),
	"temperature": newSet("unit.temperature.celsius", "unit.temperature.kelvin"),
	"volume":      newSet("unit.percent"),
}

// floatUnits maps a float property instance to its allowed units.
var floatUnits = map[string]set{
	"amperage":          newSet("unit.ampere"),
	"battery_level":     newSet("unit.percent"),
	"co2_level":         newSet("unit.ppm"),
	"electricity_meter": newSet("unit.kilowatt_hour"),
	"food_level":        newSet("unit.percent"),
	"gas_meter":         newSet("unit.cubic_meter"),
	"heat_meter":        newSet("unit.gigacalorie"),
	"humidity":          newSet("unit.percent"),
	"illumination":      newSet("unit.illumination.lux"),
	"meter":             newSet(),
	"pm1_density":       newSet("unit.density.mcg_m3"),
	"pm2.5_density":     newSet("unit.density.mcg_m3"),
	"pm10_density":      newSet("unit.density.mcg_m3"),
	"power":             newSet("unit.watt"),
	"pressure":          newSet("unit.pressure.atm", "unit.pressure.pascal", "unit.pressure.bar", "unit.pressure.mmhg"),
	"temperature":       newSet("unit.temperature.celsius", "unit.temperature.kelvin"),
	"tvoc":              newSet("unit.density.mcg_m3"),
	"voltage":           newSet("unit.volt"),
	"water_level":       newSet("unit.percent"),
	"water_meter":       newSet("unit.cubic_meter"),
}

// eventValues maps an event property instance to its allowed event values.
var eventValues = map[string]set{
	"vibration":     newSet("tilt", "fall", "vibration"),
	"open":          newSet("opened", "closed"),
	"button":        newSet("click", "double_click", "long_press"),
	"motion":        newSet("detected", "not_detected"),
	"smoke":         newSet("detected", "not_detected", "high"),
	"gas":           newSet("detected", "not_detected", "high"),
	"battery_level": newSet("low", "normal"),
	"food_level":    newSet("empty", "low", "normal"),
	"water_level":   newSet("empty", "low", "normal"),
	"water_leak":    newSet("dry", "leak"),
}
