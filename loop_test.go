package main

import (
	"bytes"
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
)

// script is a tailscaled whose state the test sets.
type script struct {
	mu    sync.Mutex
	s     state
	reads int
}

func (sc *script) set(s state) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.s = s
}

func (sc *script) read(context.Context) state {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.reads++
	return sc.s
}

func (sc *script) count() int {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.reads
}

// screen records what loop draws.
type screen struct {
	mu    sync.Mutex
	icons []icon
	tips  []string
	menus int
	notes []string
}

func (s *screen) show(i icon, tip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.icons = append(s.icons, i)
	s.tips = append(s.tips, tip)
}

func (s *screen) setMenu([]entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.menus++
}

func (s *screen) notify(title, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notes = append(s.notes, strings.TrimSpace(title+" "+body))
}

func (s *screen) snapshot() (icons []icon, tips []string, menus int, notes []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.icons), slices.Clone(s.tips), s.menus, slices.Clone(s.notes)
}

// harness runs loop in the calling synctest bubble until the test ends.
type harness struct {
	a      *app
	daemon *script
	screen *screen
}

func start(t *testing.T, s state) *harness {
	a, err := newApp(&local.Client{}, dark)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{a: a, daemon: &script{s: s}, screen: &screen{}}
	go a.loop(t.Context(), h.daemon.read, h.screen)
	return h
}

// refresh pokes loop and lets the update settle.
func (h *harness) refresh() {
	h.a.poke()
	time.Sleep(300 * time.Millisecond)
	synctest.Wait()
}

// icon names the icon loop drew last, and its tooltip.
func (h *harness) icon() (string, string) {
	icons, tips, _, _ := h.screen.snapshot()
	if len(icons) == 0 {
		return "none", ""
	}
	return iconName(h.a.icons, icons[len(icons)-1]), tips[len(tips)-1]
}

func iconName(is icons, i icon) string {
	named := map[string]icon{
		"disconnected": is.disconnected,
		"connected":    is.connected,
		"exit node":    is.exitNode,
		"exit offline": is.exitOffline,
		"copied":       is.copied,
	}
	for n, c := range named {
		if bytes.Equal(c.regular, i.regular) {
			return n
		}
	}
	for k, c := range is.connecting {
		if bytes.Equal(c.regular, i.regular) {
			return fmt.Sprintf("connecting %d", k)
		}
	}
	return "unknown"
}

func backend(st ipn.State, self *ipnstate.PeerStatus) state {
	return state{
		status: &ipnstate.Status{
			BackendState:   st.String(),
			Self:           self,
			CurrentTailnet: &ipnstate.TailnetStatus{MagicDNSSuffix: "example.ts.net"},
		},
		prefs: &ipn.Prefs{},
	}
}

func thisDevice() *ipnstate.PeerStatus {
	return &ipnstate.PeerStatus{ID: "n0", DNSName: "mac.example.ts.net.", TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.2")}}
}

func TestLoop(t *testing.T) {
	t.Run("a burst of pokes is read once, after it settles", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := start(t, backend(ipn.Running, thisDevice()))
			for range 3 {
				h.a.poke()
				synctest.Wait()
				time.Sleep(100 * time.Millisecond)
			}
			synctest.Wait()
			if got := h.daemon.count(); got != 1 {
				t.Errorf("reads = %d; want 1", got)
			}
		})
	})

	t.Run("the icon and tooltip follow the state", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := start(t, backend(ipn.Running, thisDevice()))
			h.refresh()
			if got, tip := h.icon(); got != "connected" || tip != "Tailscale: Connected" {
				t.Errorf("icon = %s, %q; want connected, \"Tailscale: Connected\"", got, tip)
			}
			h.daemon.set(backend(ipn.Stopped, nil))
			h.refresh()
			if got, tip := h.icon(); got != "disconnected" || tip != "Tailscale: Disconnected" {
				t.Errorf("icon = %s, %q; want disconnected, \"Tailscale: Disconnected\"", got, tip)
			}
		})
	})

	t.Run("the menu is rebuilt only when it changes", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := start(t, backend(ipn.Running, thisDevice()))
			h.refresh()
			h.refresh()
			if _, _, menus, _ := h.screen.snapshot(); menus != 1 {
				t.Errorf("after an unchanged state, menus = %d; want 1", menus)
			}
			h.daemon.set(backend(ipn.Stopped, nil))
			h.refresh()
			if _, _, menus, _ := h.screen.snapshot(); menus != 2 {
				t.Errorf("after a change, menus = %d; want 2", menus)
			}
		})
	})

	t.Run("a copy flashes green for one second", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := start(t, backend(ipn.Running, thisDevice()))
			h.refresh()
			signal(h.a.copied)
			synctest.Wait()
			if got, _ := h.icon(); got != "copied" {
				t.Fatalf("after the copy, icon = %s; want copied", got)
			}
			time.Sleep(999 * time.Millisecond)
			synctest.Wait()
			if got, _ := h.icon(); got != "copied" {
				t.Errorf("just under a second later, icon = %s; want copied", got)
			}
			time.Sleep(time.Millisecond)
			synctest.Wait()
			if got, _ := h.icon(); got != "connected" {
				t.Errorf("a second later, icon = %s; want connected", got)
			}
		})
	})

	t.Run("connecting spins until the state settles", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := start(t, backend(ipn.Starting, nil))
			h.refresh()
			var got []string
			for range 3 {
				n, _ := h.icon()
				got = append(got, n)
				time.Sleep(150 * time.Millisecond)
				synctest.Wait()
			}
			if want := []string{"connecting 0", "connecting 1", "connecting 2"}; !slices.Equal(got, want) {
				t.Errorf("frames = %q; want %q", got, want)
			}
			h.daemon.set(backend(ipn.Running, thisDevice()))
			h.refresh()
			icons, _, _, _ := h.screen.snapshot()
			time.Sleep(time.Second)
			synctest.Wait()
			after, _, _, _ := h.screen.snapshot()
			if n, _ := h.icon(); n != "connected" || len(after) != len(icons) {
				t.Errorf("once running, icon = %s and %d more frames; want connected and none", n, len(after)-len(icons))
			}
		})
	})

	t.Run("a change the menu did not cause is announced", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := start(t, backend(ipn.Running, thisDevice()))
			h.refresh()
			h.daemon.set(backend(ipn.Stopped, nil))
			h.refresh()
			if _, _, _, notes := h.screen.snapshot(); !slices.Equal(notes, []string{"Tailscale Disconnected"}) {
				t.Errorf("notes = %q; want [\"Tailscale Disconnected\"]", notes)
			}
		})
	})

	t.Run("a change the menu caused is not announced", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := start(t, backend(ipn.Running, thisDevice()))
			h.refresh()
			h.a.acted.Store(time.Now().UnixNano())
			h.daemon.set(backend(ipn.Stopped, nil))
			h.refresh()
			if _, _, _, notes := h.screen.snapshot(); len(notes) != 0 {
				t.Errorf("right after an action, notes = %q; want none", notes)
			}
			time.Sleep(10 * time.Second)
			h.daemon.set(backend(ipn.Running, thisDevice()))
			h.refresh()
			if _, _, _, notes := h.screen.snapshot(); !slices.Equal(notes, []string{"Tailscale Connected"}) {
				t.Errorf("ten seconds on, notes = %q; want [\"Tailscale Connected\"]", notes)
			}
		})
	})

	t.Run("key expiry is warned about at most once a day", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			self := thisDevice()
			expires := time.Now().Add(3 * 24 * time.Hour)
			self.KeyExpiry = &expires
			h := start(t, backend(ipn.Running, self))
			h.refresh()
			h.refresh()
			warnings := func() int {
				_, _, _, notes := h.screen.snapshot()
				return len(slices.DeleteFunc(notes, func(n string) bool { return !strings.Contains(n, "Key Expires") }))
			}
			if got := warnings(); got != 1 {
				t.Errorf("within a day, warnings = %d; want 1", got)
			}
			time.Sleep(24 * time.Hour)
			h.refresh()
			if got := warnings(); got != 2 {
				t.Errorf("a day later, warnings = %d; want 2", got)
			}
		})
	})
}

func TestAnnouncement(t *testing.T) {
	up := summary{running: true}
	tests := map[string]struct {
		prev, cur summary
		want      string
	}{
		"nothing changed":       {prev: up, cur: up},
		"disconnected":          {prev: up, want: "Tailscale Disconnected"},
		"connected":             {cur: up, want: "Tailscale Connected"},
		"exit node turned on":   {prev: up, cur: summary{running: true, exit: "router", exitUp: true}, want: "Using Exit Node router"},
		"exit node turned off":  {prev: summary{running: true, exit: "router", exitUp: true}, cur: up, want: "Exit Node Turned Off router"},
		"exit node went down":   {prev: summary{running: true, exit: "router", exitUp: true}, cur: summary{running: true, exit: "router"}, want: "Exit Node Offline router"},
		"exit node came back":   {prev: summary{running: true, exit: "router"}, cur: summary{running: true, exit: "router", exitUp: true}, want: "Exit Node Back Online router"},
		"disconnect wins first": {prev: summary{running: true, exit: "router", exitUp: true}, want: "Tailscale Disconnected"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			title, body, ok := announcement(tc.prev, tc.cur)
			got := strings.TrimSpace(title + " " + body)
			if ok != (tc.want != "") || got != tc.want {
				t.Errorf("announcement() = %q, %v; want %q", got, ok, tc.want)
			}
		})
	}
}
