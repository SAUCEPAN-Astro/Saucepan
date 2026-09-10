# SaucepanClient

The volunteer-telescope client for Saucepan. This is the one user-facing Go
binary a pier operator runs:

```bash
cd SaucepanClient
go build -o /tmp/saucepan ./cmd/saucepan
/tmp/saucepan run
```

`saucepan run` is the resident BOINC-style mode. It owns the local Alpaca
hardware connection, receives signed assignments over MQTT, applies the safety
gate before a slew, captures FITS frames, publishes telemetry, watches the
campaign board, uploads captures when configured, and mediates the separate
on-pier-code runner.

The same binary also provides the one-shot `status`, `constraints`, `projects`,
`board`, and `consent` commands. Those commands monitor or configure the pier;
they do not drive hardware.

The client is a separate top-level module from `SaucepanServer/`. It consumes
the task server's shared wire, safety, Alpaca, FITS, consent, and IPC contract
packages through the local monorepo dependency in `go.mod`; it does not contain
or start any server process.
