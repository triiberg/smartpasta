# Smartpasta — Clipboard History Manager (X11)

## 1. Overview

### Problem
Linux desktops lack a fast, simple, Windows-style clipboard history tool that:
- captures clipboard contents reliably in the background
- works system-wide without permissions or portals
- opens a picker UI exactly at the cursor
- does not depend on heavy GUI frameworks

Wayland-based solutions intentionally restrict these capabilities.

### Solution
**Smartpasta** is a lightweight clipboard history manager for Linux on X11, consisting of:
- a background daemon that captures clipboard contents
- a minimal, undecorated picker UI opened via a global hotkey

The tool is optimized for:
- short-lived clipboard usage (session tokens, filenames, snippets)
- speed and determinism
- power-user workflows

### Target users
- Developers
- Power users
- Users migrating from Windows
- Users who rely on system-level desktop tooling (e.g. Synergy, OBS)

---

## 2. Goals

- Capture all `Ctrl+C` clipboard copy events
- Preserve clipboard data after source applications close
- Maintain a small in-memory clipboard history
- Open a picker UI at the current cursor position
- Enable fast keyboard-based selection
- Provide an explicit “dump records” export
- Be lightweight and dependency-minimal

---

## 3. Non-goals

- Wayland support
- Cloud sync or cross-device clipboard
- Persistent clipboard history
- Image clipboard history
- OCR or content analysis
- Rich theming or GUI configuration
- Tray icon or background UI

---

## 4. Target Environment

- **Operating system:** Ubuntu Linux
- **Desktop environment:** XFCE
- **Display server:** X11 (required)
- **Wayland:** explicitly not supported
- **Architecture:** x86_64
- **Permissions:** user-level only
- **Session type:** graphical user session

---

## 5. User Stories

- When I copy text, it is immediately added to clipboard history.
- Clipboard contents survive closing the source application.
- Pressing `Win+V` opens a picker window at the cursor position.
- I can navigate clipboard history using arrow keys.
- Pressing Enter re-copies the selected item and closes the picker.
- I can dump all clipboard entries to a text file.
- Clipboard history is cleared on reboot or crash.

---

## 6. UX Specification

### Trigger
- Global hotkey: **Win+V**
- Hotkey is configured via XFCE keyboard shortcuts to launch `smartpasta-ui`.

### Popup behavior
- Undecorated, borderless X11 window
- Opens at current cursor position
- Clamped to visible screen bounds
- Focused immediately on open
- Closes automatically after selection or Esc
- Active entry (first by default) is highlighted
- Clipboard entries are ordered by most recent use (MRU):
  - Newly copied entries are placed at the top
  - Entries selected via Smartpasta are moved to the top

### Keyboard controls
- `Up / Down`: navigate entries
- `Enter`: select entry (copy to clipboard, close UI)
- `Esc`: close UI without action
- `D`: dump records to file

### Mouse controls
- Optional
- Left click selects entry 
- Scroll wheel navigates list

### Visual layout
- Minimal “box” UI
- Monospace font
- Dark background, light text
- Vertical list of entries
- Single-line preview per entry (ellipsized)
- Highlighted selection
- Footer hint line (optional)

### Input limitations
- ASCII input only
- No IME support
- No clipboard paste inside UI

---

## 7. Clipboard Capture

### Selections monitored
- X11 `CLIPBOARD` selection only
- `PRIMARY` selection is ignored

### MIME types supported
- `text/plain` only

### Size limits
- Maximum entry size: configurable
- Default: 1 MB
- Larger entries are ignored

### Deduplication
- Consecutive duplicate entries are ignored
- Non-consecutive duplicates are allowed

---

## 8. Data Model

### Clipboard Entry
- `id` (monotonically increasing integer)
- `content` (UTF-8 text)
- `created_at` (timestamp)

---

## 9. Architecture

Smartpasta consists of two processes.

### 9.1 Daemon (`smartpasta-daemon`)

Responsibilities:
- Run in the user session
- Monitor X11 clipboard ownership changes
- Request and store clipboard contents
- Maintain clipboard ownership
- Store clipboard entries in memory
- Serve requests from the UI
- Handle dump/export requests

Lifecycle:
- Started on login (XFCE autostart or systemd user service)
- Single instance
- In-memory state only
- History lost on crash or reboot

### 9.2 UI Client (`smartpasta-ui`)

Responsibilities:
- Connect to daemon via IPC
- Fetch clipboard history
- Render picker UI
- Handle user input
- Send commands to daemon
- Exit immediately after action

Lifecycle:
- Launched on demand
- No persistent state

---

## 10. IPC (Daemon ↔ UI)

### Transport
- Unix domain socket
- Path: `~/.cache/smartpasta/smartpasta.sock`
- User-local only

### Protocol
- Newline-delimited JSON
- UTF-8 encoding

### Operations

- `{"op":"history"}`
  - Response: list of clipboard entries (most recent first)

- `{"op":"select","id":<id>}`
  - Action: set clipboard to selected entry

- `{"op":"delete","id":<id>}`
  - Action: remove entry

- `{"op":"clear"}`
  - Action: clear all entries

- `{"op":"dump"}`
  - Action: dump all entries to file

---

## 11. Storage

### v1 Storage
- In-memory ring buffer
- Default maximum entries: **20**
- Configurable
- Oldest entries evicted first
- No persistence across restarts

---

## 12. Dump Records

- Triggered manually via UI (`D` key)
- Daemon writes all current entries to a text file
- Output directory: `~/smartpasta/`
- Directory is created if missing
- Filename format: dump-YYYY-MM-DD HH:MM:SS.txt
- Entries written in chronological order (oldest first)
- Entries separated by a clear delimiter

Purpose:
- Temporary export
- Debugging
- Manual reuse of session clipboard data

---

## 13. Security & Privacy

- Clipboard data stored only in memory
- No automatic persistence
- No network access
- Clipboard contents are never logged
- Dump files are explicit user action

---

## 14. Logging

- Minimal logging only
- Location: `~/.cache/smartpasta/logs/`
- Levels: error, info
- Clipboard contents must never appear in logs

---

## 15. Testing & Acceptance Criteria

- Clipboard capture works across applications
- Clipboard survives source app exit
- Picker opens at cursor on Win+V
- Keyboard navigation works
- Selection updates clipboard
- UI closes correctly
- History size limit enforced
- Dump file is created correctly

---

## 16. Implementation Constraints

- **Language:** Go
- **Display:** X11 only
- **UI:** raw X11 window (no GTK, no Qt)
- **Clipboard:** X11 APIs
- **IPC:** Unix socket + JSON
- **Dependencies:** minimal, no GUI frameworks
- **Build output:** two binaries (`smartpasta-daemon`, `smartpasta-ui`)

---

## 17. Out of Scope

- Wayland support
- Image clipboard
- Persistent history
- GUI configuration editor
- Tray icon
- Cloud or network features

## 18. Installation & Lifecycle

### Build
- Binaries are built via GitHub Actions
- Output artifacts:
  - `smartpasta-daemon`
  - `smartpasta-ui`

### Installation
- User installs binaries manually:
  - e.g. `~/.local/bin/`
- No root privileges required

### Daemon startup
One of the following is used (implementation choice):

#### Option A: XFCE Autostart (preferred for simplicity)
- A `.desktop` file is installed to: ~/.config/autostart/smartpasta-daemon.desktop
- Daemon starts automatically on login

#### Option B: systemd user service (optional)
- A user service file is installed to: ~/.config/systemd/user/smartpasta-daemon.service
- User enables it via: systemctl --user enable --now smartpasta-daemon

### UI invocation
- `smartpasta-ui` is not persistent
- It is launched on demand via a global hotkey

### Hotkey Configuration

- Default hotkey: `Win+V`
- Smartpasta does not globally grab keyboard shortcuts
- The hotkey is configured via XFCE keyboard settings:
  - Settings → Keyboard → Application Shortcuts
- Command to bind: smartpasta-ui
- Automatic hotkey configuration is out of scope for v1
