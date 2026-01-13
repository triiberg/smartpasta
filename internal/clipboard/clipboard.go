package clipboard

import (
	"errors"
	"fmt"
	"sync"

	"github.com/BurntSushi/xgb"
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

func NewManager(maxBytes int, display string, logger func(string, ...any)) (*Manager, error) {
	conn, err := openConn(display)
	if err != nil {
		if display == "" {
			return nil, fmt.Errorf("connect to X11: %w", err)
		}
		return nil, fmt.Errorf("connect to X11 display %q: %w", display, err)
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
		0,
		window,
		screen.Root,
		0,
		0,
		1,
		1,
		0,
		xproto.WindowClassInputOnly,
		screen.RootVisual,
		xproto.CwEventMask,
		[]uint32{
			xproto.EventMaskPropertyChange | xproto.EventMaskStructureNotify,
			// Selection events (SelectionNotify/Clear/Request) are delivered to
			// the owner/requestor, so we keep an explicit event mask to ensure
			// the hidden window is eligible for property updates tied to selections.
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

	manager := &Manager{
		conn:     conn,
		window:   window,
		atoms:    atoms,
		maxBytes: maxBytes,
		logger:   logger,
	}

	return manager, nil
}

func openConn(display string) (*xgb.Conn, error) {
	if display == "" {
		return xgb.NewConn()
	}
	return xgb.NewConnDisplay(display)
}

func (m *Manager) Close() {
	if m.conn != nil {
		m.conn.Close()
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

	// Prime the loop by requesting the current clipboard contents. This is event-driven:
	// SelectionNotify will deliver the data, and onNew should re-acquire ownership via
	// SetClipboard.
	m.requestClipboard()

	for {
		event, err := m.conn.WaitForEvent()
		if err != nil {
			return ErrConnectionClosed
		}

		switch ev := event.(type) {
		case xproto.SelectionClearEvent:
			// Another application took clipboard ownership. Immediately request the
			// new owner's data via ConvertSelection.
			if ev.Selection != m.atoms["CLIPBOARD"] {
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

	// Store the clipboard contents. The callback is responsible for re-acquiring
	// ownership (SetSelectionOwner) so we continue receiving SelectionClear events.
	onNew(content)
}

func (m *Manager) handleSelectionRequest(ev xproto.SelectionRequestEvent) {
	if ev.Selection != m.atoms["CLIPBOARD"] {
		return
	}

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
	}

	if ev.Target == m.atoms["TARGETS"] {
		targets := []xproto.Atom{m.atoms["UTF8_STRING"], m.atoms["TEXT"], m.atoms["STRING"], m.atoms["TARGETS"]}
		data := make([]byte, len(targets)*4)
		for i, atom := range targets {
			xgb.Put32(data[i*4:], uint32(atom))
		}
		err := xproto.ChangePropertyChecked(
			m.conn,
			xproto.PropModeReplace,
			ev.Requestor,
			property,
			xproto.AtomAtom,
			32,
			uint32(len(targets)),
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
