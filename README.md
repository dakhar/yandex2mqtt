# yandex2mqtt

A bridge between an MQTT broker and **Yandex Smart Home** (Alice): it exposes
your MQTT-controlled devices to Yandex as a cloud provider, translating between
Yandex capability/property states and MQTT topics, and acts as the OAuth2
provider Yandex links against.

This is a Go rewrite of the original Node.js service, with a typed configuration
layer, a persistent token store, and per-device value mapping validated against
the Yandex reference schema.

## Features

- **Yandex Smart Home provider API** — `HEAD /v1.0`, `GET /user/devices`,
  `POST /user/devices/query`, `POST /user/devices/action`, `POST /user/unlink`,
  mounted under `/provider`.
- **OAuth2 authorization server** (`go-oauth2`) — authorization_code, password,
  client_credentials and refresh grants; tokens persisted in **SQLite**
  (pure-Go `modernc.org/sqlite`, no CGO) so they survive restarts.
- **MQTT bridge** (`paho`) — subscribes to device state topics, publishes
  capability actions, auto-reconnects and re-subscribes.
- **State notifications** — pushes device state changes to the Yandex callback
  API (IPv4-forced, as the smart-home network breaks IPv6 TLS to Yandex).
- **Typed config** — secrets and infrastructure from environment variables
  (12-factor, Ansible-Vault friendly); the device catalog from a YAML file
  (with anchors), validated against the Yandex capability/property schema at
  startup.
- **Request logging** with the real client IP (via `X-Forwarded-For` /
  `X-Real-IP` behind a reverse proxy).

## Configuration

Secrets and infrastructure come from the environment — see
[`.env.example`](.env.example). The device catalog is a YAML file (default
`./data/devices.yaml`) — see [`config.example.yaml`](config.example.yaml) for
the schema and anchor usage.

Structural catalog errors (unknown capability/property types) abort startup;
unknown instances/units/values are logged as warnings (forward-compatible with
new Yandex additions).

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
HTTP (TLS handled upstream); otherwise provide `WEB_TLS_CERT` / `WEB_TLS_KEY`.

## Development

```sh
go test ./...
go vet ./...
```

## Layout

```
cmd/yandex2mqtt   entrypoint and wiring
internal/config   typed config: env (secrets) + YAML (device catalog)
internal/device   device domain model, value conversions/mappings, schema
internal/mqtt     MQTT bridge (subscriptions + publish)
internal/yandex   provider API handlers + state-notification client
internal/auth     OAuth2 server + session login/web pages
internal/store    SQLite token store
internal/httplog  request-logging middleware (real client IP)
tools/convert     one-shot migrator: legacy config.js -> devices.yaml
```

## License

MIT
