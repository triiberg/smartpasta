# smartpasta-daemon

Clipboard history daemon for X11.

## Build

```bash
go build -o smartpasta-daemon ./cmd/smartpasta-daemon
```

## Run

```bash
./smartpasta-daemon
```

The daemon listens on `~/.cache/smartpasta/smartpasta.sock` and stores clipboard history in memory only. Dump files are written to `~/smartpasta/` when requested by the UI.
