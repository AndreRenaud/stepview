package main

import (
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

// menuCommand is an action in the menu bar. It is also the bit for the
// command in the masks given to publishMenuState, so there are at most 32.
type menuCommand int

const (
	cmdOpen menuCommand = iota
	cmdExit
	cmdSidebar
	cmdFit
	cmdFitSelection
	cmdIso
	cmdTop
	cmdFront
	cmdRight
	cmdSpin
	cmdShowAll
	cmdNormal
	cmdWireframe
	cmdHighQuality
)

// menuItem is an entry in a menu: a command, a separator (title "-") or a
// submenu.
type menuItem struct {
	cmd   menuCommand
	title string
	// key is a lower case letter or digit, typed with the primary modifier
	// (Command on macOS, Control elsewhere) and those below; 0 for none.
	key   rune
	shift bool
	alt   bool // Option on macOS
	ctrl  bool // Control on macOS, Alt elsewhere
	sub   []menuItem
}

type menu struct {
	title string
	items []menuItem
}

var separator = menuItem{title: "-"}

var menus = []menu{
	{"File", []menuItem{
		{cmd: cmdOpen, title: "Open…", key: 'o'},
		separator,
		{cmd: cmdExit, title: "Exit", key: 'q'},
	}},
	{"View", []menuItem{
		{cmd: cmdSidebar, title: "Show Sidebar", key: 's', ctrl: true},
		separator,
		{cmd: cmdFit, title: "Fit All", key: 'f'},
		{cmd: cmdFitSelection, title: "Fit Selection", key: 'f', shift: true},
		separator,
		{cmd: cmdIso, title: "Iso", key: '1'},
		{cmd: cmdTop, title: "Top", key: '2'},
		{cmd: cmdFront, title: "Front", key: '3'},
		{cmd: cmdRight, title: "Right", key: '4'},
		{cmd: cmdSpin, title: "Spin", key: 'r'},
		separator,
		{title: "Quality", sub: []menuItem{
			{cmd: cmdNormal, title: "Normal", key: '1', alt: true},
			{cmd: cmdWireframe, title: "Wireframe", key: '2', alt: true},
			{cmd: cmdHighQuality, title: "High Quality", key: '3', alt: true},
		}},
		separator,
		{cmd: cmdShowAll, title: "Show All Parts", key: 'h', shift: true},
	}},
}

// ebitenKey returns the key for a menuItem key.
func ebitenKey(r rune) (ebiten.Key, bool) {
	switch {
	case r >= 'a' && r <= 'z':
		return ebiten.KeyA + ebiten.Key(r-'a'), true
	case r >= '0' && r <= '9':
		return ebiten.KeyDigit0 + ebiten.Key(r-'0'), true
	}
	return 0, false
}

// pressedShortcut returns the command whose shortcut was just typed. It is
// for when there is no native menu bar to see them. mac selects the macOS
// modifiers; elsewhere Control is the primary modifier and Alt stands in for
// both alt and ctrl.
func pressedShortcut(mac bool) (menuCommand, bool) {
	primary := ebiten.KeyControl
	if mac {
		primary = ebiten.KeyMeta
	}
	if !ebiten.IsKeyPressed(primary) {
		return 0, false
	}
	shift := ebiten.IsKeyPressed(ebiten.KeyShift)
	alt := ebiten.IsKeyPressed(ebiten.KeyAlt)
	ctrl := ebiten.IsKeyPressed(ebiten.KeyControl)
	matches := func(it menuItem) bool {
		if it.shift != shift {
			return false
		}
		if mac {
			return it.alt == alt && it.ctrl == ctrl
		}
		return (it.alt || it.ctrl) == alt
	}
	var find func(items []menuItem) (menuCommand, bool)
	find = func(items []menuItem) (menuCommand, bool) {
		for _, it := range items {
			if it.sub != nil {
				if c, ok := find(it.sub); ok {
					return c, true
				}
				continue
			}
			if k, ok := ebitenKey(it.key); ok && matches(it) && inpututil.IsKeyJustPressed(k) {
				return it.cmd, true
			}
		}
		return 0, false
	}
	for _, m := range menus {
		if c, ok := find(m.items); ok {
			return c, true
		}
	}
	return 0, false
}
