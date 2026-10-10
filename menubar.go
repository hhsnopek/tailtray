package main

import (
	"context"
	"time"

	"fyne.io/systray"
)

// menuBar is the menu bar on macOS and the system tray on Linux.
type menuBar struct {
	run             func(func(context.Context) error)
	online, offline []byte
	cancel          context.CancelFunc // stops the click handlers of the menu shown
}

// show sets the icon as a template when it has one, so macOS tints it to
// match the menu bar.
func (m *menuBar) show(i icon, tip string) {
	setTooltip(tip)
	if i.template == nil {
		systray.SetIcon(i.regular)
		return
	}
	systray.SetTemplateIcon(i.template, i.regular)
}

func (m *menuBar) setMenu(es []entry) {
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	systray.ResetMenu()
	for _, e := range es {
		m.add(ctx, nil, e)
	}
}

func (m *menuBar) notify(title, body string) { notify(title, body) }

func (m *menuBar) add(ctx context.Context, parent *systray.MenuItem, e entry) {
	if e.sep {
		if parent == nil {
			systray.AddSeparator()
			return
		}
		parent.AddSeparator()
		return
	}
	var it *systray.MenuItem
	switch {
	case parent == nil && e.toggle:
		it = systray.AddMenuItemCheckbox(e.title, "", e.on)
	case parent == nil:
		it = systray.AddMenuItem(e.title, "")
	case e.toggle:
		it = parent.AddSubMenuItemCheckbox(e.title, "", e.on)
	default:
		it = parent.AddSubMenuItem(e.title, "")
	}
	switch e.dot {
	case onlineDot:
		it.SetIcon(m.online)
	case offlineDot:
		it.SetIcon(m.offline)
	}
	if e.disabled {
		it.Disable()
	}
	if len(e.sub) > 0 {
		time.Sleep(subMenuDelay)
		for _, c := range e.sub {
			m.add(ctx, it, c)
		}
	}
	if e.do == nil || e.disabled {
		return
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-it.ClickedCh:
				go m.run(e.do)
			}
		}
	}()
}
