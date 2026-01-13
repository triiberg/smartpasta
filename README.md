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

If your service manager does not provide a `DISPLAY` environment, you can pass the target display explicitly:

```bash
./smartpasta-daemon -display :0
```

The daemon listens on `~/.cache/smartpasta/smartpasta.sock` and stores clipboard history in memory only. Dump files are written to `~/smartpasta/` when requested by the UI.
