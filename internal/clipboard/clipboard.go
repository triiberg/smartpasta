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

func (m *Manager) logf(format string, args ...any) {
	if m.logger == nil {
		return
	}
	m.logger("[clipboard] "+format, args...)
}

func (m *Manager) atomName(atom xproto.Atom) string {
	for name, value := range m.atoms {
		if value == atom {
			return name
		}
	}
	return fmt.Sprintf("atom(%d)", atom)
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
		"ATOM",
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

	manager.logf("daemon startup window=%d display=%q maxBytes=%d", window, display, maxBytes)
	manager.logf("atom initialized name=CLIPBOARD id=%d", atoms["CLIPBOARD"])
	manager.logf("atom initialized name=ATOM id=%d", atoms["ATOM"])
	manager.logf("atom initialized name=UTF8_STRING id=%d", atoms["UTF8_STRING"])
	manager.logf("atom initialized name=STRING id=%d", atoms["STRING"])
	manager.logf("atom initialized name=TARGETS id=%d", atoms["TARGETS"])

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

	m.logf("SetSelectionOwner selection=CLIPBOARD window=%d", m.window)
	if err := xproto.SetSelectionOwnerChecked(
		m.conn,
		m.window,
		m.atoms["CLIPBOARD"],
		xproto.TimeCurrentTime,
	).Check(); err != nil {
		return err
	}
	m.conn.Sync()

	// 🔍 VERIFY OWNERSHIP IMMEDIATELY
	if owner, err := xproto.GetSelectionOwner(
		m.conn,
		m.atoms["CLIPBOARD"],
	).Reply(); err == nil {
		m.logf(
			"post-SetClipboard owner=%d (me=%d)",
			owner.Owner,
			m.window,
		)
	}

	return nil
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
			m.logf("SelectionClear window=%d selection=%s(%d) owner=%d", m.window, m.atomName(ev.Selection), ev.Selection, ev.Owner)
			// Another application took clipboard ownership. Immediately request the
			// new owner's data via ConvertSelection.
			if ev.Selection != m.atoms["CLIPBOARD"] {
				continue
			}
			m.requestClipboard()
		case xproto.SelectionNotifyEvent:
			m.logf("SelectionNotify window=%d selection=%s(%d) target=%s(%d) property=%s(%d)", m.window, m.atomName(ev.Selection), ev.Selection, m.atomName(ev.Target), ev.Target, m.atomName(ev.Property), ev.Property)
			m.handleSelectionNotify(ev, onNew)
		case xproto.SelectionRequestEvent:
			m.logf("SelectionRequest window=%d selection=%s(%d) target=%s(%d) requestor=%d property=%s(%d)", m.window, m.atomName(ev.Selection), ev.Selection, m.atomName(ev.Target), ev.Target, ev.Requestor, m.atomName(ev.Property), ev.Property)
			m.handleSelectionRequest(ev)
		}
	}
}

func (m *Manager) requestClipboard() {
	owner, err := xproto.GetSelectionOwner(m.conn, m.atoms["CLIPBOARD"]).Reply()
	if err == nil {
		m.logf("clipboard owner window=%d", owner.Owner)
	}

	m.requestClipboardTarget(m.atoms["UTF8_STRING"])
}

func (m *Manager) requestClipboardTarget(target xproto.Atom) {
	if target == m.atoms["UTF8_STRING"] {
		m.logf(
			"ConvertSelection request window=%d selection=CLIPBOARD target=UTF8_STRING property=None",
			m.window,
		)
	} else {
		m.logf(
			"ConvertSelection request window=%d selection=CLIPBOARD target=%s(%d) property=None",
			m.window,
			m.atomName(target),
			target,
		)
	}
	_ = xproto.ConvertSelectionChecked(
		m.conn,
		m.window,
		m.atoms["CLIPBOARD"],
		target,
		xproto.AtomNone, // ✅ REQUIRED
		xproto.TimeCurrentTime,
	).Check()
}

func (m *Manager) requestTargets() {
	m.logf(
		"ConvertSelection request window=%d selection=CLIPBOARD target=TARGETS property=None",
		m.window,
	)
	_ = xproto.ConvertSelectionChecked(
		m.conn,
		m.window,
		m.atoms["CLIPBOARD"],
		m.atoms["TARGETS"],
		xproto.AtomNone,
		xproto.TimeCurrentTime,
	).Check()
}

func (m *Manager) handleSelectionNotify(ev xproto.SelectionNotifyEvent, onNew func(string)) {
	if ev.Selection != m.atoms["CLIPBOARD"] {
		m.logf("SelectionNotify ignored selection=%s(%d)", m.atomName(ev.Selection), ev.Selection)
		return
	}
	if ev.Property == xproto.AtomNone {
		m.logf("SelectionNotify ignored property=NONE")
		if ev.Target == m.atoms["UTF8_STRING"] {
			m.requestTargets()
		}
		return
	}

	if ev.Target == m.atoms["TARGETS"] {
		m.handleTargetsNotify(ev)
		return
	}

	reply, err := xproto.GetProperty(m.conn, true, m.window, ev.Property, xproto.AtomAny, 0, uint32(m.maxBytes)).Reply()
	if err != nil {
		m.logf("get property failed: %v", err)
		return
	}

	if len(reply.Value) == 0 {
		m.logf("clipboard data reception length=0")
		return
	}
	if len(reply.Value) > m.maxBytes {
		m.logf("clipboard data reception length=%d exceeds maxBytes=%d", len(reply.Value), m.maxBytes)
		return
	}

	content := string(reply.Value)
	if content == "" {
		m.logf("clipboard data reception length=0 after decode")
		return
	}
	m.logf("clipboard data reception length=%d", len(reply.Value))

	// Store the clipboard contents. The callback is responsible for re-acquiring
	// ownership (SetSelectionOwner) so we continue receiving SelectionClear events.
	onNew(content)
}

func (m *Manager) handleTargetsNotify(ev xproto.SelectionNotifyEvent) {
	reply, err := xproto.GetProperty(m.conn, true, m.window, ev.Property, xproto.AtomAny, 0, uint32(m.maxBytes)).Reply()
	if err != nil {
		m.logf("get property failed: %v", err)
		return
	}

	available := unpackAtoms32(reply.Value)
	target := selectBestTarget(available, []xproto.Atom{
		m.atoms["UTF8_STRING"],
		m.atoms["STRING"],
		m.atoms["TEXT"],
	})
	if target == xproto.AtomNone {
		m.logf("clipboard targets missing expected formats length=%d", len(available))
		return
	}

	m.requestClipboardTarget(target)
}

func (m *Manager) handleSelectionRequest(ev xproto.SelectionRequestEvent) {
	property := ev.Property
	if property == xproto.AtomNone {
		property = m.atoms["SMARTPASTA_CLIP"]
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
		m.conn.Sync()
		m.logf("SelectionNotify sent requestor=%d selection=%s(%d) target=%s(%d) property=%s(%d)", ev.Requestor, m.atomName(ev.Selection), ev.Selection, m.atomName(ev.Target), ev.Target, m.atomName(prop), prop)
	}

	if ev.Selection != m.atoms["CLIPBOARD"] {
		// Always respond with SelectionNotify, even if we are not the owner for
		// this selection. This keeps requestors from hanging while awaiting a
		// reply.
		sendNotify(xproto.AtomNone)
		return
	}

	if ev.Target == m.atoms["TARGETS"] {
		targets := []xproto.Atom{
			m.atoms["TARGETS"],
			m.atoms["UTF8_STRING"],
			m.atoms["STRING"],
			m.atoms["TEXT"],
		}
		data := packAtoms32(targets)
		m.logf("clipboard data serving target=%s(%d) length=%d", m.atomName(ev.Target), ev.Target, len(data))
		err := xproto.ChangePropertyChecked(
			m.conn,
			xproto.PropModeReplace,
			ev.Requestor,
			property,
			m.atoms["ATOM"],
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
	// X11 selection flow:
	// 1) We previously called SetSelectionOwner to claim CLIPBOARD.
	// 2) A requester sends SelectionRequest with a target (UTF8_STRING/STRING/TEXT).
	// 3) We write the current clipboard payload into the requestor's property.
	// 4) We send SelectionNotify to signal completion (even on failure).
	bytes := []byte(content)
	m.logf("clipboard data serving target=%s(%d) length=%d", m.atomName(ev.Target), ev.Target, len(bytes))
	propertyType := ev.Target
	if ev.Target == m.atoms["STRING"] {
		propertyType = m.atoms["STRING"]
	}
	if ev.Target == m.atoms["TEXT"] {
		propertyType = m.atoms["UTF8_STRING"]
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

func packAtoms32(atoms []xproto.Atom) []byte {
	data := make([]byte, len(atoms)*4)
	for i, atom := range atoms {
		xgb.Put32(data[i*4:], uint32(atom))
	}
	return data
}

func unpackAtoms32(data []byte) []xproto.Atom {
	count := len(data) / 4
	atoms := make([]xproto.Atom, 0, count)
	for i := 0; i < count; i++ {
		atoms = append(atoms, xproto.Atom(xgb.Get32(data[i*4:])))
	}
	return atoms
}

func selectBestTarget(available []xproto.Atom, preferred []xproto.Atom) xproto.Atom {
	availableSet := make(map[xproto.Atom]struct{}, len(available))
	for _, atom := range available {
		availableSet[atom] = struct{}{}
	}
	for _, atom := range preferred {
		if _, ok := availableSet[atom]; ok {
			return atom
		}
	}
	return xproto.AtomNone
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
