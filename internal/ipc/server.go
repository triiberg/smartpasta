package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"smartpasta/internal/history"
)

type Request struct {
	Op      string `json:"op"`
	ID      int64  `json:"id,omitempty"`
	Content string `json:"content,omitempty"`
}

type Response struct {
	Ok      bool            `json:"ok"`
	Error   string          `json:"error,omitempty"`
	Entries []history.Entry `json:"entries,omitempty"`
}

type Server struct {
	listener      net.Listener
	socketPath    string
	history       *history.History
	setClipboard  func(string) error
	logger        func(string, ...any)
	dumpDirectory string
}

func NewServer(socketPath string, dumpDir string, historyStore *history.History, setClipboard func(string) error, logger func(string, ...any)) (*Server, error) {
	if socketPath == "" {
		return nil, fmt.Errorf("socket path required")
	}
	if dumpDir == "" {
		return nil, fmt.Errorf("dump directory required")
	}

	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}
	if err := os.MkdirAll(dumpDir, 0o700); err != nil {
		return nil, fmt.Errorf("create dump dir: %w", err)
	}

	listener, err := listenUnix(socketPath)
	if err != nil {
		return nil, err
	}

	return &Server{
		listener:      listener,
		socketPath:    socketPath,
		history:       historyStore,
		setClipboard:  setClipboard,
		logger:        logger,
		dumpDirectory: dumpDir,
	}, nil
}

func (s *Server) Close() error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

func (s *Server) Serve() error {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var req Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.writeResponse(conn, Response{Ok: false, Error: "invalid json"})
			continue
		}

		s.handleRequest(conn, req)
	}
}

func (s *Server) handleRequest(conn net.Conn, req Request) {
	switch req.Op {
	case "history":
		entries := s.history.ListMRU()
		s.writeResponse(conn, Response{Ok: true, Entries: entries})
	case "select":
		entry, err := s.history.Select(req.ID)
		if err != nil {
			s.writeResponse(conn, Response{Ok: false, Error: "not found"})
			return
		}
		if s.setClipboard != nil {
			if err := s.setClipboard(entry.Content); err != nil {
				s.writeResponse(conn, Response{Ok: false, Error: "clipboard error"})
				return
			}
		}
		s.writeResponse(conn, Response{Ok: true})
	case "delete":
		if err := s.history.Delete(req.ID); err != nil {
			s.writeResponse(conn, Response{Ok: false, Error: "not found"})
			return
		}
		s.writeResponse(conn, Response{Ok: true})
	case "clear":
		s.history.Clear()
		s.writeResponse(conn, Response{Ok: true})
	case "dump":
		filename := filepath.Join(s.dumpDirectory, dumpFilename(time.Now()))
		if err := dumpEntries(filename, s.history.ListChronological()); err != nil {
			if s.logger != nil {
				s.logger("dump failed: %v", err)
			}
			s.writeResponse(conn, Response{Ok: false, Error: "dump failed"})
			return
		}
		s.writeResponse(conn, Response{Ok: true})
	default:
		s.writeResponse(conn, Response{Ok: false, Error: "unknown op"})
	}
}

func (s *Server) writeResponse(conn net.Conn, resp Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(data, '\n'))
}

func listenUnix(socketPath string) (net.Listener, error) {
	if _, err := os.Stat(socketPath); err == nil {
		if conn, err := net.DialTimeout("unix", socketPath, 250*time.Millisecond); err == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("daemon already running")
		}
		_ = os.Remove(socketPath)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on unix socket: %w", err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}
	return listener, nil
}

func dumpFilename(t time.Time) string {
	return fmt.Sprintf("dump-%s.txt", t.Format("2006-01-02 15:04:05"))
}

func dumpEntries(path string, entries []history.Entry) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	for i, entry := range entries {
		if _, err := writer.WriteString(entry.Content); err != nil {
			return err
		}
		if i < len(entries)-1 {
			if _, err := writer.WriteString("\n-----\n"); err != nil {
				return err
			}
		}
	}
	return writer.Flush()
}
