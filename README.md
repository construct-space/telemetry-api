# Construct Telemetry API

Ingests pre-aggregated daily deltas from the desktop app (Bearer auth) and serves admin rollups to Oracle (`X-Internal-Secret`). Go, MySQL.

Part of [Construct](https://github.com/construct-space), the platform behind construct.space, published as it ran in September 2026. The organisation README maps the other services.

## Run

```
go run .
```

Copy `.env.sample` to `.env` and fill in the values; secrets are marked `change-me`.
A `Dockerfile` and a `captain-definition` are included: the service ran on CapRover.

## License

MIT, see `LICENSE`.