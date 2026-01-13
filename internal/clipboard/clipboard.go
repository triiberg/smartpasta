package clipboard

import (
	"errors"
	"fmt"
	"sync"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xfixes"
	"github.com/BurntSushi/xgb/xproto"
)

var ErrConnectionClosed = errors.New("x11 connection closed")

type Manager struct {
	conn     *xgb.Conn
	window   xproto.Window
	atoms    map[string]xproto.Atom
	mu       sync.Mutex
	current  string
	maxBytes int
	logger   func(string, ...any)
}

func NewManager(maxBytes int, logger func(string, ...any)) (*Manager, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("connect to X11: %w", err)
	}

	if err := xfixes.Init(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("init xfixes: %w", err)
	}

	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)
	window, err := xproto.NewWindowId(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("new window id: %w", err)
	}

	err = xproto.CreateWindowChecked(
		conn,
		xproto.WindowClassInputOnly,
		window,
		screen.Root,
		0, 0, 1, 1,
		0,
		screen.RootVisual,
		xproto.CwEventMask,
		[]uint32{
			xproto.EventMaskPropertyChange,
		},
	).Check()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("create window: %w", err)
	}

	atoms, err := internAtoms(conn, []string{
		"CLIPBOARD",
		"UTF8_STRING",
		"TARGETS",
		"TEXT",
		"STRING",
		"SMARTPASTA_CLIP",
	})
	if err != nil {
		conn.Close()
		return nil, err
	}

	err = xfixes.SelectSelectionInputChecked(
		conn,
		window,
		atoms["CLIPBOARD"],
		xfixes.SelectionEventMaskSetSelectionOwner,
	).Check()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("select selection input: %w", err)
	}

	manager := &Manager{
		conn:     conn,
		window:   window,
		atoms:    atoms,
		maxBytes: maxBytes,
		logger:   logger,
	}

	return manager, nil
}

func (m *Manager) Close() {
	if m.conn != nil {
		_ = m.conn.Close()
	}
}

func (m *Manager) SetClipboard(content string) error {
	m.mu.Lock()
	m.current = content
	m.mu.Unlock()

	return xproto.SetSelectionOwnerChecked(m.conn, m.window, m.atoms["CLIPBOARD"], xproto.TimeCurrentTime).Check()
}

func (m *Manager) Current() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

func (m *Manager) Run(onNew func(string)) error {
	if onNew == nil {
		return errors.New("onNew callback required")
	}

	m.requestClipboard()

	for {
		event, err := m.conn.WaitForEvent()
		if err != nil {
			return ErrConnectionClosed
		}

		switch ev := event.(type) {
		case xfixes.SelectionNotifyEvent:
			if ev.Selection != m.atoms["CLIPBOARD"] {
				continue
			}
			if ev.Owner == m.window {
				continue
			}
			m.requestClipboard()
		case xproto.SelectionNotifyEvent:
			m.handleSelectionNotify(ev, onNew)
		case xproto.SelectionRequestEvent:
			m.handleSelectionRequest(ev)
		}
	}
}

func (m *Manager) requestClipboard() {
	_ = xproto.ConvertSelectionChecked(
		m.conn,
		m.window,
		m.atoms["CLIPBOARD"],
		m.atoms["UTF8_STRING"],
		m.atoms["SMARTPASTA_CLIP"],
		xproto.TimeCurrentTime,
	).Check()
}

func (m *Manager) handleSelectionNotify(ev xproto.SelectionNotifyEvent, onNew func(string)) {
	if ev.Property == xproto.AtomNone {
		return
	}
	if ev.Selection != m.atoms["CLIPBOARD"] {
		return
	}

	reply, err := xproto.GetProperty(m.conn, true, m.window, ev.Property, xproto.AtomAny, 0, uint32(m.maxBytes)).Reply()
	if err != nil {
		if m.logger != nil {
			m.logger("get property failed: %v", err)
		}
		return
	}

	if len(reply.Value) == 0 {
		return
	}
	if len(reply.Value) > m.maxBytes {
		return
	}

	content := string(reply.Value)
	if content == "" {
		return
	}

	onNew(content)
}

func (m *Manager) handleSelectionRequest(ev xproto.SelectionRequestEvent) {
	property := ev.Property
	if property == xproto.AtomNone {
		property = ev.Target
	}

	sendNotify := func(prop xproto.Atom) {
		notify := xproto.SelectionNotifyEvent{
			Time:      ev.Time,
			Requestor: ev.Requestor,
			Selection: ev.Selection,
			Target:    ev.Target,
			Property:  prop,
		}
		_ = xproto.SendEventChecked(m.conn, false, ev.Requestor, 0, string(notify.Bytes())).Check()
		_ = m.conn.Flush()
	}

	if ev.Target == m.atoms["TARGETS"] {
		targets := []xproto.Atom{m.atoms["UTF8_STRING"], m.atoms["TEXT"], m.atoms["STRING"], m.atoms["TARGETS"]}
		data := make([]uint32, len(targets))
		for i, atom := range targets {
			data[i] = uint32(atom)
		}
		err := xproto.ChangePropertyChecked(
			m.conn,
			xproto.PropModeReplace,
			ev.Requestor,
			property,
			xproto.AtomAtom,
			32,
			uint32(len(data)),
			data,
		).Check()
		if err != nil {
			sendNotify(xproto.AtomNone)
			return
		}
		sendNotify(property)
		return
	}

	if ev.Target != m.atoms["UTF8_STRING"] && ev.Target != m.atoms["TEXT"] && ev.Target != m.atoms["STRING"] {
		sendNotify(xproto.AtomNone)
		return
	}

	content := m.Current()
	if content == "" {
		sendNotify(xproto.AtomNone)
		return
	}

	bytes := []byte(content)
	propertyType := m.atoms["UTF8_STRING"]
	if ev.Target == m.atoms["STRING"] {
		propertyType = xproto.AtomString
	}
	err := xproto.ChangePropertyChecked(
		m.conn,
		xproto.PropModeReplace,
		ev.Requestor,
		property,
		propertyType,
		8,
		uint32(len(bytes)),
		bytes,
	).Check()
	if err != nil {
		sendNotify(xproto.AtomNone)
		return
	}

	sendNotify(property)
}

func internAtoms(conn *xgb.Conn, names []string) (map[string]xproto.Atom, error) {
	atoms := make(map[string]xproto.Atom, len(names))
	for _, name := range names {
		cookie := xproto.InternAtom(conn, true, uint16(len(name)), name)
		reply, err := cookie.Reply()
		if err != nil {
			return nil, fmt.Errorf("intern atom %s: %w", name, err)
		}
		atoms[name] = reply.Atom
	}
	return atoms, nil
}
