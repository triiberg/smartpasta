package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"

	"smartpasta/internal/history"
)

const (
	defaultWidth      = 640
	defaultLineHeight = 18
	padding           = 10
	footerHeight      = 18
	maxPreviewChars   = 80
)

const (
	keysymUp     xproto.Keysym = 0xff52
	keysymDown   xproto.Keysym = 0xff54
	keysymReturn xproto.Keysym = 0xff0d
	keysymEscape xproto.Keysym = 0xff1b
	keysymD      xproto.Keysym = 0x0044
	keysymd      xproto.Keysym = 0x0064
)

type request struct {
	Op string `json:"op"`
	ID int64  `json:"id,omitempty"`
}

type response struct {
	Ok      bool            `json:"ok"`
	Error   string          `json:"error,omitempty"`
	Entries []history.Entry `json:"entries,omitempty"`
}

type ipcClient struct {
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
}

func newIPCClient(socketPath string) (*ipcClient, error) {
	conn, err := net.DialTimeout("unix", socketPath, 500*time.Millisecond)
	if err != nil {
		return nil, err
	}
	return &ipcClient{
		conn:   conn,
		reader: bufio.NewReader(conn),
		writer: bufio.NewWriter(conn),
	}, nil
}

func (c *ipcClient) Close() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

func (c *ipcClient) do(req request, resp *response) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := c.writer.WriteString(string(data) + "\n"); err != nil {
		return err
	}
	if err := c.writer.Flush(); err != nil {
		return err
	}
	line, err := c.reader.ReadString('\n')
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(strings.TrimSpace(line)), resp)
}

func (c *ipcClient) history() ([]history.Entry, error) {
	var resp response
	if err := c.do(request{Op: "history"}, &resp); err != nil {
		return nil, err
	}
	if !resp.Ok {
		return nil, errors.New(resp.Error)
	}
	return resp.Entries, nil
}

func (c *ipcClient) selectEntry(id int64) error {
	var resp response
	if err := c.do(request{Op: "select", ID: id}, &resp); err != nil {
		return err
	}
	if !resp.Ok {
		return errors.New(resp.Error)
	}
	return nil
}

func (c *ipcClient) dump() error {
	var resp response
	if err := c.do(request{Op: "dump"}, &resp); err != nil {
		return err
	}
	if !resp.Ok {
		return errors.New(resp.Error)
	}
	return nil
}

type keymap struct {
	minKeycode xproto.Keycode
	maxKeycode xproto.Keycode
	perCode    int
	keysyms    []xproto.Keysym
}

func newKeymap(conn *xgb.Conn) (*keymap, error) {
	setup := xproto.Setup(conn)
	minKeycode := setup.MinKeycode
	maxKeycode := setup.MaxKeycode
	count := int(maxKeycode-minKeycode) + 1
	resp, err := xproto.GetKeyboardMapping(conn, minKeycode, byte(count)).Reply()
	if err != nil {
		return nil, err
	}
	return &keymap{
		minKeycode: minKeycode,
		maxKeycode: maxKeycode,
		perCode:    int(resp.KeysymsPerKeycode),
		keysyms:    resp.Keysyms,
	}, nil
}

func (k *keymap) matches(keycode xproto.Keycode, targets ...xproto.Keysym) bool {
	if keycode < k.minKeycode || keycode > k.maxKeycode {
		return false
	}
	start := int(keycode-k.minKeycode) * k.perCode
	end := start + k.perCode
	if start < 0 || end > len(k.keysyms) {
		return false
	}
	for _, sym := range k.keysyms[start:end] {
		for _, target := range targets {
			if sym == target {
				return true
			}
		}
	}
	return false
}

type uiState struct {
	entries       []history.Entry
	selectedIndex int
	visibleTop    int
	visibleCount  int
	width         int
	height        int
}

func main() {
	display := flag.String("display", "", "X11 display to use (overrides DISPLAY)")
	flag.Parse()

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "failed to determine cache directory")
			os.Exit(1)
		}
		cacheDir = filepath.Join(homeDir, ".cache")
	}
	socketPath := filepath.Join(cacheDir, "smartpasta", "smartpasta.sock")

	client, err := newIPCClient(socketPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to connect to smartpasta daemon")
		os.Exit(1)
	}
	defer client.Close()

	entries, err := client.history()
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to fetch history")
		os.Exit(1)
	}

	conn, err := openConn(*display)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer conn.Close()

	keymap, err := newKeymap(conn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to read keyboard mapping")
		os.Exit(1)
	}

	ui, err := newUI(conn, entries)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := ui.run(conn, keymap, client); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func openConn(display string) (*xgb.Conn, error) {
	if display == "" {
		return xgb.NewConn()
	}
	conn, err := xgb.NewConnDisplay(display)
	if err != nil {
		return nil, fmt.Errorf("connect to X11 display %q: %w", display, err)
	}
	return conn, nil
}

type ui struct {
	window           xproto.Window
	state            uiState
	bgGC             xproto.Gcontext
	textGC           xproto.Gcontext
	highlightGC      xproto.Gcontext
	highlightTextGC  xproto.Gcontext
	footerTextGC     xproto.Gcontext
	font             xproto.Font
	lineHeight       int
	footerText       string
	selectionEnabled bool
}

func newUI(conn *xgb.Conn, entries []history.Entry) (*ui, error) {
	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)

	width := defaultWidth
	if width > int(screen.WidthInPixels) {
		width = int(screen.WidthInPixels)
	}

	visibleCount := len(entries)
	if visibleCount == 0 {
		visibleCount = 1
	}
	maxVisible := (int(screen.HeightInPixels) - (2*padding + footerHeight)) / defaultLineHeight
	if maxVisible < 1 {
		maxVisible = 1
	}
	if visibleCount > maxVisible {
		visibleCount = maxVisible
	}

	height := padding*2 + visibleCount*defaultLineHeight + footerHeight
	if height > int(screen.HeightInPixels) {
		height = int(screen.HeightInPixels)
	}

	root := screen.Root
	query, err := xproto.QueryPointer(conn, root).Reply()
	if err != nil {
		return nil, fmt.Errorf("query pointer: %w", err)
	}

	x := int(query.RootX)
	y := int(query.RootY)
	if x+width > int(screen.WidthInPixels) {
		x = int(screen.WidthInPixels) - width
	}
	if y+height > int(screen.HeightInPixels) {
		y = int(screen.HeightInPixels) - height
	}
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	window, err := xproto.NewWindowId(conn)
	if err != nil {
		return nil, err
	}

	mask := uint32(xproto.CwBackPixel | xproto.CwEventMask | xproto.CwOverrideRedirect)
	values := []uint32{
		screen.BlackPixel,
		xproto.EventMaskExposure | xproto.EventMaskKeyPress,
		1,
	}

	if err := xproto.CreateWindowChecked(
		conn,
		uint8(screen.RootDepth),
		window,
		root,
		int16(x),
		int16(y),
		uint16(width),
		uint16(height),
		0,
		xproto.WindowClassInputOutput,
		screen.RootVisual,
		mask,
		values,
	).Check(); err != nil {
		return nil, fmt.Errorf("create window: %w", err)
	}

	font, err := xproto.NewFontId(conn)
	if err == nil {
		_ = xproto.OpenFontChecked(conn, font, uint16(len("fixed")), "fixed").Check()
	}

	colors, err := newColors(conn, screen.DefaultColormap)
	if err != nil {
		return nil, err
	}

	bgGC, err := createGC(conn, window, colors.background, colors.background, font)
	if err != nil {
		return nil, err
	}
	textGC, err := createGC(conn, window, colors.text, colors.background, font)
	if err != nil {
		return nil, err
	}
	highlightGC, err := createGC(conn, window, colors.highlight, colors.highlight, font)
	if err != nil {
		return nil, err
	}
	highlightTextGC, err := createGC(conn, window, colors.highlightText, colors.highlight, font)
	if err != nil {
		return nil, err
	}
	footerTextGC, err := createGC(conn, window, colors.footerText, colors.background, font)
	if err != nil {
		return nil, err
	}

	return &ui{
		window: window,
		state: uiState{
			entries:       entries,
			selectedIndex: 0,
			visibleTop:    0,
			visibleCount:  visibleCount,
			width:         width,
			height:        height,
		},
		bgGC:             bgGC,
		textGC:           textGC,
		highlightGC:      highlightGC,
		highlightTextGC:  highlightTextGC,
		footerTextGC:     footerTextGC,
		font:             font,
		lineHeight:       defaultLineHeight,
		footerText:       "Enter: select  Esc: close  D: dump",
		selectionEnabled: len(entries) > 0,
	}, nil
}

type uiColors struct {
	background    uint32
	text          uint32
	highlight     uint32
	highlightText uint32
	footerText    uint32
}

func newColors(conn *xgb.Conn, colormap xproto.Colormap) (*uiColors, error) {
	background, err := allocColor(conn, colormap, "1e1e1e")
	if err != nil {
		return nil, err
	}
	text, err := allocColor(conn, colormap, "e0e0e0")
	if err != nil {
		return nil, err
	}
	highlight, err := allocColor(conn, colormap, "2f5d8a")
	if err != nil {
		return nil, err
	}
	highlightText, err := allocColor(conn, colormap, "ffffff")
	if err != nil {
		return nil, err
	}
	footerText, err := allocColor(conn, colormap, "aaaaaa")
	if err != nil {
		return nil, err
	}
	return &uiColors{
		background:    background,
		text:          text,
		highlight:     highlight,
		highlightText: highlightText,
		footerText:    footerText,
	}, nil
}

func allocColor(conn *xgb.Conn, colormap xproto.Colormap, hex string) (uint32, error) {
	if len(hex) != 6 {
		return 0, fmt.Errorf("invalid color: %s", hex)
	}
	parse := func(s string) (uint16, error) {
		var v uint8
		if _, err := fmt.Sscanf(s, "%02x", &v); err != nil {
			return 0, err
		}
		return uint16(v) * 257, nil
	}
	r, err := parse(hex[0:2])
	if err != nil {
		return 0, err
	}
	g, err := parse(hex[2:4])
	if err != nil {
		return 0, err
	}
	b, err := parse(hex[4:6])
	if err != nil {
		return 0, err
	}
	reply, err := xproto.AllocColor(conn, colormap, r, g, b).Reply()
	if err != nil {
		return 0, err
	}
	return reply.Pixel, nil
}

func createGC(conn *xgb.Conn, window xproto.Window, fg uint32, bg uint32, font xproto.Font) (xproto.Gcontext, error) {
	gc, err := xproto.NewGcontextId(conn)
	if err != nil {
		return 0, err
	}
	mask := uint32(xproto.GcForeground | xproto.GcBackground)
	values := []uint32{fg, bg}
	if font != 0 {
		mask |= xproto.GcFont
		values = append(values, uint32(font))
	}
	if err := xproto.CreateGCChecked(conn, gc, xproto.Drawable(window), mask, values).Check(); err != nil {
		return 0, err
	}
	return gc, nil
}

func (u *ui) run(conn *xgb.Conn, keymap *keymap, client *ipcClient) error {
	if err := xproto.MapWindowChecked(conn, u.window).Check(); err != nil {
		return err
	}
	_ = xproto.SetInputFocusChecked(conn, xproto.InputFocusPointerRoot, u.window, xproto.TimeCurrentTime).Check()
	u.draw(conn)

	for {
		event, err := conn.WaitForEvent()
		if err != nil {
			return err
		}
		switch ev := event.(type) {
		case xproto.ExposeEvent:
			u.draw(conn)
		case xproto.KeyPressEvent:
			if keymap.matches(ev.Detail, keysymEscape) {
				return nil
			}
			if keymap.matches(ev.Detail, keysymUp) {
				u.moveSelection(-1)
				u.draw(conn)
				continue
			}
			if keymap.matches(ev.Detail, keysymDown) {
				u.moveSelection(1)
				u.draw(conn)
				continue
			}
			if keymap.matches(ev.Detail, keysymReturn) {
				if u.selectionEnabled {
					entry := u.state.entries[u.state.selectedIndex]
					_ = client.selectEntry(entry.ID)
				}
				return nil
			}
			if keymap.matches(ev.Detail, keysymD, keysymd) {
				_ = client.dump()
				return nil
			}
		}
	}
}

func (u *ui) moveSelection(delta int) {
	if !u.selectionEnabled {
		return
	}
	count := len(u.state.entries)
	if count == 0 {
		return
	}
	newIndex := u.state.selectedIndex + delta
	if newIndex < 0 {
		newIndex = 0
	}
	if newIndex >= count {
		newIndex = count - 1
	}
	u.state.selectedIndex = newIndex

	if newIndex < u.state.visibleTop {
		u.state.visibleTop = newIndex
	}
	if newIndex >= u.state.visibleTop+u.state.visibleCount {
		u.state.visibleTop = newIndex - u.state.visibleCount + 1
	}
}

func (u *ui) draw(conn *xgb.Conn) {
	rect := xproto.Rectangle{X: 0, Y: 0, Width: uint16(u.state.width), Height: uint16(u.state.height)}
	_ = xproto.PolyFillRectangleChecked(conn, xproto.Drawable(u.window), u.bgGC, []xproto.Rectangle{rect}).Check()

	textY := padding + u.lineHeight - 4
	start := u.state.visibleTop
	end := start + u.state.visibleCount
	if end > len(u.state.entries) {
		end = len(u.state.entries)
	}
	if len(u.state.entries) == 0 {
		msg := "No clipboard history"
		u.drawText(conn, padding, textY, msg, u.textGC)
		u.drawFooter(conn)
		return
	}

	for i := start; i < end; i++ {
		offset := i - start
		y := padding + offset*u.lineHeight
		if i == u.state.selectedIndex {
			hRect := xproto.Rectangle{X: 0, Y: int16(y), Width: uint16(u.state.width), Height: uint16(u.lineHeight)}
			_ = xproto.PolyFillRectangleChecked(conn, xproto.Drawable(u.window), u.highlightGC, []xproto.Rectangle{hRect}).Check()
			u.drawText(conn, padding, y+u.lineHeight-4, previewLine(u.state.entries[i].Content), u.highlightTextGC)
			continue
		}
		u.drawText(conn, padding, y+u.lineHeight-4, previewLine(u.state.entries[i].Content), u.textGC)
	}
	u.drawFooter(conn)
}

func (u *ui) drawFooter(conn *xgb.Conn) {
	footerY := u.state.height - padding
	u.drawText(conn, padding, footerY, u.footerText, u.footerTextGC)
}

func (u *ui) drawText(conn *xgb.Conn, x int, y int, text string, gc xproto.Gcontext) {
	if text == "" {
		return
	}
	bytes := []byte(text)
	if len(bytes) > 255 {
		bytes = bytes[:255]
	}
	_ = xproto.ImageText8Checked(conn, uint8(len(bytes)), xproto.Drawable(u.window), gc, int16(x), int16(y), string(bytes)).Check()
}

func previewLine(content string) string {
	line := strings.ReplaceAll(content, "\n", " ")
	line = strings.TrimSpace(line)
	return ellipsize(line, maxPreviewChars)
}

func ellipsize(text string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}
