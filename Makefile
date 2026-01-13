APP_NAME := smartpasta
BIN_DIR := $(HOME)/.local/bin
CACHE_DIR := $(HOME)/.cache/smartpasta
AUTOSTART_DIR := $(HOME)/.config/autostart

DAEMON := smartpasta-daemon
UI := smartpasta-ui

DAEMON_SRC := ./cmd/smartpasta-daemon
UI_SRC := ./cmd/smartpasta-ui

.PHONY: all build install install-daemon autostart clean

all: build

## Build both binaries
build:
	@echo "==> Building $(DAEMON)"
	go build -o $(DAEMON) $(DAEMON_SRC)
	@echo "==> Building $(UI)"
	go build -o $(UI) $(UI_SRC)

## Install binaries to ~/.local/bin
install: build
	@echo "==> Installing binaries to $(BIN_DIR)"
	mkdir -p $(BIN_DIR)
	cp $(DAEMON) $(BIN_DIR)/
	cp $(UI) $(BIN_DIR)/
	@echo "==> Installed:"
	@ls -l $(BIN_DIR)/$(DAEMON) $(BIN_DIR)/$(UI)

## Install daemon autostart for XFCE
autostart:
	@echo "==> Installing XFCE autostart entry"
	mkdir -p $(AUTOSTART_DIR)
	printf "[Desktop Entry]\nType=Application\nName=Smartpasta Daemon\nExec=%s/%s\nOnlyShowIn=XFCE;\nX-GNOME-Autostart-enabled=true\n" \
		"$(BIN_DIR)" "$(DAEMON)" \
		> $(AUTOSTART_DIR)/$(DAEMON).desktop
	@echo "==> Autostart installed:"
	@cat $(AUTOSTART_DIR)/$(DAEMON).desktop

## Remove build artifacts
clean:
	rm -f $(DAEMON) $(UI)
