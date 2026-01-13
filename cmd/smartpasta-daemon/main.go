package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"smartpasta/internal/clipboard"
	"smartpasta/internal/history"
	"smartpasta/internal/ipc"
	"smartpasta/internal/logging"
)

func main() {
	maxEntries := flag.Int("max-entries", history.DefaultMaxEntries, "maximum clipboard entries")
	maxBytes := flag.Int("max-bytes", history.DefaultMaxBytes, "maximum clipboard entry size in bytes")
	flag.Parse()

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to determine home directory")
		os.Exit(1)
	}

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = filepath.Join(homeDir, ".cache")
	}
	cacheDir = filepath.Join(cacheDir, "smartpasta")
	dumpDir := filepath.Join(homeDir, "smartpasta")

	logger, err := logging.NewLogger(cacheDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to initialize logger")
		os.Exit(1)
	}

	historyStore := history.New(*maxEntries, *maxBytes)

	clipboardManager, err := clipboard.NewManager(*maxBytes, logger.Errorf)
	if err != nil {
		logger.Errorf("clipboard init failed: %v", err)
		fmt.Fprintln(os.Stderr, "failed to initialize clipboard")
		os.Exit(1)
	}
	defer clipboardManager.Close()

	onNew := func(content string) {
		entry, added := historyStore.Add(content)
		if !added {
			return
		}
		logger.Infof("captured clipboard entry %d", entry.ID)
		if err := clipboardManager.SetClipboard(content); err != nil {
			logger.Errorf("failed to set clipboard owner: %v", err)
		}
	}

	server, err := ipc.NewServer(filepath.Join(cacheDir, "smartpasta.sock"), dumpDir, historyStore, clipboardManager.SetClipboard, logger.Errorf)
	if err != nil {
		logger.Errorf("ipc server error: %v", err)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer server.Close()

	errCh := make(chan error, 2)

	go func() {
		errCh <- clipboardManager.Run(onNew)
	}()

	go func() {
		errCh <- server.Serve()
	}()

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-signalCh:
		logger.Infof("received signal %s, shutting down", sig.String())
	case err := <-errCh:
		if err != nil {
			logger.Errorf("daemon error: %v", err)
		}
	}

	shutdownDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(shutdownDeadline) {
		time.Sleep(50 * time.Millisecond)
	}
}
