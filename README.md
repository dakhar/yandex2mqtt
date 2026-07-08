# yandex2mqtt

A bridge that exposes your **MQTT** and **openHAB** devices to **Yandex Smart
Home** (Alice). It acts as a Yandex cloud provider (OAuth2 + the device REST
API), translates between Yandex capability/property states and the underlying
transports, and ships a multi-user web UI for managing devices, rooms and
discovery.

Go rewrite of the original Node.js service: typed config, a normalized SQLite
catalog validated against the Yandex reference schema, hot-reloadable device
registry, and pluggable transports.

## Features

### Provider & auth
- **Yandex Smart Home provider API** — `HEAD /v1.0`, `GET /user/devices`,
  `POST /user/devices/query`, `POST /user/devices/action`, `POST /user/unlink`
  (mounted under `/provider`), guarded by real bearer-token verification.
- **OAuth2 authorization server** (`go-oauth2`); tokens persisted in **SQLite**
  (pure-Go `modernc.org/sqlite`, no CGO) so the Alice link survives restarts.
- **Notifications** — proactive `callback/state` on device changes and
  `callback/discovery` when a user's device list changes (so Alice re-syncs
  without a manual skill refresh). IPv4-forced for the smart-home network.

### Transports (connectors)
- **MQTT bridge** (`paho`) — subscribes to state topics, publishes actions,
  auto-reconnects/re-subscribes. Supports **JSON-payload state** extraction by
  dot-path (`ENERGY.Power`, `state`), so several instances share one topic
  (Tasmota/z2m/openBeken).
- **openHAB connector** (REST/SSE) — state in via `/rest/events`, commands out
  via `POST /rest/items`, initial-state sync on (re)connect.
- Both run in parallel; each device picks its transport. Server connection
  settings are admin-editable in the UI and applied by **hot reconnect**.

### openHAB discovery
- **Semantic model** — Equipment groups become one composite device, Location
  groups set the room, `Group:Switch/Dimmer/Color` aggregation points are mapped
  by their `groupType`, and openHAB's `semantics` (Equipment vs Point)
  disambiguates. Infers lights, thermostats, curtains, fans, sensors, meters, …
- **`yahome` metadata override** — declare a point's mapping explicitly
  (`yahome="on_off"`, `yahome="mode"`) when tags aren't enough.
- **Flat mode** — a per-user toggle to list every item as its own device
  instead of composing equipment.
- **`stateDescription`-driven** range bounds and fan-speed value maps.
- Per-user tag filter, ignore list (with a restore page), and "configure before
  add".

### Web management UI
- **Room board** — device cards dragged between rooms.
- **Device builder** — compose capabilities/properties, per-instance MQTT/
  openHAB bindings, a **value-mapping table** (Yandex value ↔ device value, no
  need to know the enum), range **percentage inversion** (curtains), a
  **status → `error_code`** binding, and `retrievable`/`reportable` flags.
- **Bilingual RU/EN** labels for every type/instance/mode (wording from the
  Yandex docs), with an in-UI language toggle.
- **Settings** — export/import/reset the whole user config (rooms + devices +
  settings), plus the admin server-connection editor.
- **Multi-user** — an admin creates users; each sees only their own devices and
  rooms; Alice is linked per user via OAuth.

### Catalog
- Normalized **SQLite** schema (rooms, devices, capabilities, properties, MQTT
  topics, value mappings, openHAB bindings, error bindings, per-user settings).
  The DB is authoritative and **hot-reloaded** on every edit; a YAML file seeds
  it on first run only. Startup validates against the Yandex schema.

## Configuration

Secrets and bootstrap come from the environment — see [`.env.example`](.env.example)
(admin user, session secret, OAuth client, and the *initial* MQTT/openHAB
connection). The device catalog lives in the DB and is managed through the web
UI; [`config.example.yaml`](config.example.yaml) documents the optional
first-run seed file.

MQTT/openHAB connection settings can be overridden by an admin in
**Settings → Servers** (stored in the DB, applied without a restart); an empty
DB value falls back to the environment.

### Environment variables

| Variable | Purpose | Default |
| --- | --- | --- |
| `SESSION_SECRET` | session-cookie key (required) | — |
| `ADMIN_USERNAME`, `ADMIN_PASSWORD` | bootstrap admin (required) | — |
| `OAUTH_CLIENT_ID`, `OAUTH_CLIENT_SECRET` | OAuth client for Yandex (required) | — |
| `MQTT_HOST`, `MQTT_PORT` | broker | `localhost`, `1883` |
| `MQTT_USER`, `MQTT_PASSWORD` | broker credentials | — |
| `OPENHAB_URL` | openHAB base URL (enables the transport) | — |
| `OPENHAB_TOKEN` / `OPENHAB_TOKEN_FILE` | openHAB API token (file preferred) | — |
| `WEB_PORT` | listen port | `80` |
| `WEB_BEHIND_PROXY` | serve plain HTTP behind a TLS proxy | `false` |
| `WEB_TLS_CERT`, `WEB_TLS_KEY` | own TLS (when not behind a proxy) | — |
| `YANDEX_SKILL_ID`, `YANDEX_OAUTH_TOKEN`, `YANDEX_USER_ID` | proactive callbacks | — |
| `DB_PATH` | SQLite path | `./data/yandex2mqtt.db` |
| `DEVICES_FILE` | first-run YAML seed | `./data/devices.yaml` |
| `LOG_LEVEL` | log level | `info` |

`MQTT_*` and `OPENHAB_*` are the *initial* values; a non-empty DB override from
the UI takes precedence. Everything else is read once at startup.

Structural catalog errors (unknown capability/property types) abort startup;
unknown instances/units/values are warnings (forward-compatible with new Yandex
additions).

## Run

With Docker (behind a TLS-terminating reverse proxy):

```sh
cp .env.example .env    # fill in secrets
docker compose up -d
```

Or directly:

```sh
export $(grep -v '^#' .env | xargs)
go run ./cmd/yandex2mqtt
```

The service listens on `WEB_PORT`. Set `WEB_BEHIND_PROXY=true` to serve plain
HTTP (TLS handled upstream). The management UI is at `/app`; log in with the
admin credentials from `.env`.

## Version

The build version is logged at startup, printed by `yandex2mqtt -version`, served
at `GET /version`, and shown on the settings page. Inject it at build time:

```sh
docker compose build --build-arg VERSION=$(git describe --tags --always --dirty)
```

Without the build-arg it falls back to the embedded VCS commit (plain `go build`
in the repo) or `dev`.

## Development

```sh
go test ./...
go vet ./...
```

## Layout

```
cmd/yandex2mqtt   entrypoint and wiring
internal/config   typed config: env (secrets) + YAML (first-run seed)
internal/device   device domain model, value conversions/mappings, schema, labels
internal/mqtt     MQTT bridge (subscriptions, publish, JSON-path extraction)
internal/openhab  openHAB connector (REST/SSE) + semantic/flat discovery
internal/yandex   provider API handlers + state/discovery notification client
internal/auth     OAuth2 server + session login
internal/store    SQLite: tokens, users, normalized catalog, settings, app config
internal/web      management UI: board, device builder, discovery, settings
internal/httplog  request-logging middleware (real client IP)
tools/convert     one-shot migrator: legacy config.js -> devices.yaml
```

## License

MIT
