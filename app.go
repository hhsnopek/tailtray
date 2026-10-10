package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"fyne.io/systray"
	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/tailcfg"
	"tailscale.com/tailcfg/nodecap"
)

type app struct {
	lc    *local.Client
	icons icons
	ssh   sshTargets

	refresh chan struct{}
	copied  chan struct{}

	// acted is when a menu action last ran, in Unix nanoseconds, so loop
	// announces only the changes the menu did not cause.
	acted atomic.Int64

	// Owned by loop, through fetch.
	ports   []listener
	portsAt time.Time
}

func newApp(lc *local.Client, p palette) (*app, error) {
	is, err := newIcons(p)
	if err != nil {
		return nil, err
	}
	return &app{
		lc:      lc,
		icons:   is,
		refresh: make(chan struct{}, 1),
		copied:  make(chan struct{}, 1),
	}, nil
}

func (a *app) ready() {
	bar := &menuBar{run: a.run, online: a.icons.online, offline: a.icons.offline}
	bar.show(a.icons.disconnected, "Tailscale")
	ctx := context.Background()
	go a.watch(ctx)
	go a.receive(ctx)
	go a.loop(ctx, a.fetch, bar)
	a.poke()
}

// poke asks loop to reread the state; requests made while one is pending
// collapse into it.
func (a *app) poke() { signal(a.refresh) }

func signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

// desktop is what loop draws on: menuBar, or a recorder in tests.
type desktop interface {
	show(i icon, tip string)
	setMenu([]entry)
	notify(title, body string)
}

// loop owns the menu and the icon, reading the state with read. Peer updates
// arrive in bursts, so it waits for them to settle before reading.
func (a *app) loop(ctx context.Context, read func(context.Context) state, d desktop) {
	var (
		settle, restore <-chan time.Time
		spin            *time.Ticker
		frame           int
		current         = a.icons.disconnected
		tip             string
		shown           []entry
		last            *summary
		warned          time.Time
	)
	defer func() {
		if spin != nil {
			spin.Stop()
		}
	}()
	spinning := func() <-chan time.Time {
		if spin == nil {
			return nil
		}
		return spin.C
	}
	// showing is the icon for now, which a copy's flash briefly replaces.
	showing := func() icon {
		if spin != nil {
			return a.icons.connecting[frame]
		}
		return current
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.refresh:
			if settle == nil {
				settle = time.After(300 * time.Millisecond)
			}
		case <-settle:
			settle = nil
			s := read(ctx)
			current, tip = a.look(s)
			starting := s.status != nil && s.status.BackendState == ipn.Starting.String()
			switch {
			case starting && spin == nil:
				spin = time.NewTicker(150 * time.Millisecond)
				frame = 0
			case !starting && spin != nil:
				spin.Stop()
				spin = nil
			}
			if restore == nil {
				d.show(showing(), tip)
			}
			if es := a.entries(s); !same(es, shown) {
				d.setMenu(es)
				shown = es
			}

			sum := summarize(s)
			if last != nil && time.Since(time.Unix(0, a.acted.Load())) > 10*time.Second {
				if title, body, ok := announcement(*last, sum); ok {
					d.notify(title, body)
				}
			}
			last = &sum
			if s.status != nil {
				if w := expiry(s.status.Self, time.Now()); w != "" && time.Since(warned) > 24*time.Hour {
					d.notify("Tailscale "+w, "Extend or disable key expiry in the admin console.")
					warned = time.Now()
				}
			}
		case <-spinning():
			frame = (frame + 1) % len(a.icons.connecting)
			if restore == nil {
				d.show(showing(), tip)
			}
		case <-a.copied:
			d.show(a.icons.copied, tip)
			restore = time.After(time.Second)
		case <-restore:
			restore = nil
			d.show(showing(), tip)
		}
	}
}

// summary is the part of the state worth a notification when it changes.
type summary struct {
	running bool
	exit    string // name of the exit node in use, or ""
	exitUp  bool
}

func summarize(s state) summary {
	if !s.running() {
		return summary{}
	}
	sum := summary{running: true}
	if ens := s.status.ExitNodeStatus; ens != nil && ens.ID != "" {
		sum.exit, sum.exitUp = string(ens.ID), ens.Online
		if p := peer(s.status, ens.ID); p != nil {
			sum.exit = name(p)
		}
	}
	return sum
}

// announcement is the notification a change from prev to cur deserves, if
// any.
func announcement(prev, cur summary) (title, body string, ok bool) {
	switch {
	case prev.running && !cur.running:
		return "Tailscale Disconnected", "", true
	case !prev.running && cur.running:
		return "Tailscale Connected", "", true
	case prev.exit != cur.exit && cur.exit == "":
		return "Exit Node Turned Off", prev.exit, true
	case prev.exit != cur.exit:
		return "Using Exit Node " + cur.exit, "", true
	case prev.exitUp && !cur.exitUp:
		return "Exit Node Offline", cur.exit, true
	case !prev.exitUp && cur.exitUp:
		return "Exit Node Back Online", cur.exit, true
	}
	return "", "", false
}

// look is the icon and tooltip for s.
func (a *app) look(s state) (icon, string) {
	if !s.running() {
		return a.icons.disconnected, "Tailscale: Disconnected"
	}
	ens := s.status.ExitNodeStatus
	switch {
	case ens == nil || ens.ID == "":
		return a.icons.connected, "Tailscale: Connected"
	case ens.Online:
		return a.icons.exitNode, "Tailscale: Using an Exit Node"
	}
	return a.icons.exitOffline, "Tailscale: Exit Node Offline"
}

func (a *app) fetch(ctx context.Context) state {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var (
		s   state
		err error
	)
	if s.status, err = a.lc.Status(ctx); err != nil {
		log.Print(err)
		return state{}
	}
	if s.profile, s.profiles, err = a.lc.ProfileStatus(ctx); err != nil {
		s.readonly = local.IsAccessDeniedError(err)
		log.Print(err)
	}
	if s.prefs, err = a.lc.GetPrefs(ctx); err != nil {
		log.Print(err)
	}
	if !s.running() || s.status.Self == nil {
		return s
	}
	if s.serve, err = a.lc.GetServeConfig(ctx); err != nil {
		log.Print(err)
	}
	if s.targets, err = a.lc.FileTargets(ctx); err != nil {
		log.Print(err)
	}
	if s.status.Self.CapMap.Contains(nodecap.SuggestExitNodeUI) {
		if sg, err := a.lc.SuggestExitNode(ctx); err == nil {
			s.suggest = sg.ID
		}
	}
	// Scanning for listening ports runs a subprocess, so the result is
	// reused across the bursts of updates peers cause.
	if time.Since(a.portsAt) > 15*time.Second {
		if a.ports, err = listeners(); err != nil {
			log.Print(err)
		}
		a.portsAt = time.Now()
	}
	s.listeners = a.ports
	return s
}

func (a *app) run(do func(context.Context) error) {
	a.acted.Store(time.Now().UnixNano())
	if err := do(context.Background()); err != nil {
		log.Print(err)
		notify("Tailtray", err.Error())
	}
	a.acted.Store(time.Now().UnixNano())
	a.poke()
}

// watch rereads the state on every change tailscaled reports, and opens the
// login page when it asks for one.
func (a *app) watch(ctx context.Context) {
	for {
		if err := a.watchOnce(ctx); err != nil {
			log.Print(err)
		}
		a.poke()
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (a *app) watchOnce(ctx context.Context) (err error) {
	w, err := a.lc.WatchIPNBus(ctx, ipn.NotifyInitialState|ipn.NotifyPeerChanges)
	if err != nil {
		return fmt.Errorf("watching tailscaled: %w", err)
	}
	defer func() { err = errors.Join(err, w.Close()) }()
	for {
		n, err := w.Next()
		if err != nil {
			return fmt.Errorf("watching tailscaled: %w", err)
		}
		if u := n.BrowseToURL; u != nil && *u != "" {
			if err := openURL(*u); err != nil {
				log.Print(err)
			}
		}
		a.poke()
	}
}

// receive saves files sent to this device with Taildrop into Downloads,
// as the Tailscale apps do; tailscaled otherwise holds them until
// `tailscale file get`.
func (a *app) receive(ctx context.Context) {
	for {
		waiting, err := a.lc.AwaitWaitingFiles(ctx, 10*time.Minute)
		if err != nil {
			log.Printf("waiting for Taildrop files: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
			continue
		}
		for _, f := range waiting {
			path, err := a.save(ctx, f.Name)
			if err != nil {
				log.Print(err)
				notify("Couldn't Save "+f.Name, err.Error())
				continue
			}
			notify("Received "+filepath.Base(path), "Saved to "+filepath.Dir(path))
		}
	}
}

func (a *app) save(ctx context.Context, name string) (_ string, err error) {
	dir, err := downloads()
	if err != nil {
		return "", fmt.Errorf("finding Downloads: %w", err)
	}
	rc, _, err := a.lc.GetWaitingFile(ctx, name)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, rc.Close()) }()
	f, err := createUnique(dir, filepath.Base(name))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, rc); err != nil {
		return "", errors.Join(fmt.Errorf("writing %s: %w", f.Name(), err), f.Close(), os.Remove(f.Name()))
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("writing %s: %w", f.Name(), err)
	}
	if err := a.lc.DeleteWaitingFile(ctx, name); err != nil {
		return f.Name(), fmt.Errorf("saved %s, but tailscaled still holds it: %w", f.Name(), err)
	}
	return f.Name(), nil
}

// createUnique creates name in dir, or "name (2).ext" and on when that is
// taken, so a received file never replaces one already there.
func createUnique(dir, name string) (*os.File, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		n := name
		if i > 1 {
			n = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		f, err := os.OpenFile(filepath.Join(dir, n), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if !errors.Is(err, fs.ErrExist) {
			return f, err
		}
	}
}

func (a *app) quit(context.Context) error {
	systray.Quit()
	return nil
}

func (a *app) copy(text string) func(context.Context) error {
	return func(context.Context) error {
		if err := copyText(text); err != nil {
			return fmt.Errorf("copying: %w", err)
		}
		signal(a.copied)
		return nil
	}
}

func (a *app) open(link string) func(context.Context) error {
	return func(context.Context) error { return openURL(link) }
}

func (a *app) edit(mp *ipn.MaskedPrefs) func(context.Context) error {
	return func(ctx context.Context) error {
		if _, err := a.lc.EditPrefs(ctx, mp); err != nil {
			return fmt.Errorf("changing settings: %w", err)
		}
		return nil
	}
}

func (a *app) login(ctx context.Context) error {
	if err := a.lc.StartLoginInteractive(ctx); err != nil {
		return fmt.Errorf("starting login: %w", err)
	}
	return nil
}

// useExitNode turns the exit node off, remembering it, or back on.
func (a *app) useExitNode(on bool) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := a.lc.SetUseExitNode(ctx, on); err != nil {
			return fmt.Errorf("switching the exit node: %w", err)
		}
		return nil
	}
}

func (a *app) setExitNode(id tailcfg.StableNodeID) func(context.Context) error {
	return a.edit(&ipn.MaskedPrefs{Prefs: ipn.Prefs{ExitNodeID: id}, ExitNodeIDSet: true})
}

func (a *app) ping(device string, ip netip.Addr) func(context.Context) error {
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		r, err := a.lc.Ping(ctx, ip, tailcfg.PingDisco)
		if err != nil {
			return fmt.Errorf("pinging %s: %w", device, err)
		}
		if r.Err != "" {
			return fmt.Errorf("pinging %s: %s", device, r.Err)
		}
		via := "direct to " + r.Endpoint
		switch {
		case r.PeerRelay != "":
			via = "through peer relay " + r.PeerRelay
		case r.DERPRegionCode != "":
			via = "relayed via " + r.DERPRegionCode
		}
		notify(fmt.Sprintf("%s: %.0f ms", device, r.LatencySeconds*1000), via)
		return nil
	}
}

func (a *app) openSSH(device string) func(context.Context) error {
	return func(context.Context) error {
		dest, ok, err := askText("SSH to "+device+" as user@host:", a.ssh.get(device))
		if err != nil || !ok {
			return err
		}
		dest = strings.TrimSpace(dest)
		if err := openTerminal("ssh", dest); err != nil {
			return fmt.Errorf("opening a terminal: %w", err)
		}
		return a.ssh.set(device, dest)
	}
}

func (a *app) switchProfile(id ipn.ProfileID) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := a.lc.SwitchProfile(ctx, id); err != nil {
			return fmt.Errorf("switching accounts: %w", err)
		}
		return nil
	}
}

func (a *app) addAccount(current string) func(context.Context) error {
	return func(ctx context.Context) error {
		if !confirm("Add another Tailscale account? This device leaves "+current+"'s tailnet until you switch back.", "Add Account") {
			return nil
		}
		if err := a.lc.SwitchToEmptyProfile(ctx); err != nil {
			return fmt.Errorf("adding an account: %w", err)
		}
		return a.login(ctx)
	}
}

func (a *app) logout(current string) func(context.Context) error {
	return func(ctx context.Context) error {
		if !confirm("Log out of "+current+"? This device leaves the tailnet until you log in again.", "Log Out") {
			return nil
		}
		if err := a.lc.Logout(ctx); err != nil {
			return fmt.Errorf("logging out: %w", err)
		}
		return nil
	}
}

func (a *app) removeProfile(id ipn.ProfileID, account string) func(context.Context) error {
	return func(ctx context.Context) error {
		if !confirm("Remove "+account+" from this device?", "Remove") {
			return nil
		}
		if err := a.lc.DeleteProfile(ctx, id); err != nil {
			return fmt.Errorf("removing %s: %w", account, err)
		}
		return nil
	}
}

// editServe applies edit to a fresh copy of the serve config. The copy's
// ETag makes tailscaled refuse the write if anything changed it meanwhile.
func (a *app) editServe(ctx context.Context, edit func(*ipn.ServeConfig) error) error {
	sc, err := a.lc.GetServeConfig(ctx)
	if err != nil {
		return err
	}
	if err := edit(sc); err != nil {
		return err
	}
	if err := a.lc.SetServeConfig(ctx, sc); err != nil {
		return fmt.Errorf("saving the serve config: %w", err)
	}
	return nil
}

func (a *app) serveLocal(host, suffix string) func(context.Context) error {
	return func(ctx context.Context) error {
		in, ok, err := askText("Serve a local port to your tailnet over HTTPS.\n\nLocal port:", "3000")
		if err != nil || !ok {
			return err
		}
		port, err := strconv.ParseUint(strings.TrimSpace(in), 10, 16)
		if err != nil || port == 0 {
			return fmt.Errorf("%q is not a port number", in)
		}
		return a.servePort(host, suffix, uint16(port))(ctx)
	}
}

// servePort serves http://127.0.0.1:port over HTTPS on the first Funnel
// port not already serving.
func (a *app) servePort(host, suffix string, port uint16) func(context.Context) error {
	return func(ctx context.Context) error {
		var link string
		err := a.editServe(ctx, func(sc *ipn.ServeConfig) error {
			for _, p := range funnelPorts {
				if _, used := sc.TCP[p]; used {
					continue
				}
				sc.SetWebHandler(&ipn.HTTPHandler{Proxy: fmt.Sprintf("http://127.0.0.1:%d", port)}, host, p, "/", true, suffix)
				link = serveURL(host, p, "/")
				return nil
			}
			return errors.New("ports 443, 8443 and 10000 are all serving already")
		})
		if err != nil {
			return err
		}
		notify(fmt.Sprintf("Serving Port %d", port), link)
		return nil
	}
}

func (a *app) stopServing(host string, port uint16, mount string) func(context.Context) error {
	return func(ctx context.Context) error {
		return a.editServe(ctx, func(sc *ipn.ServeConfig) error {
			sc.RemoveWebHandler(host, port, []string{mount}, true)
			return nil
		})
	}
}

func (a *app) stopForwarding(port uint16) func(context.Context) error {
	return func(ctx context.Context) error {
		return a.editServe(ctx, func(sc *ipn.ServeConfig) error {
			sc.RemoveTCPForwarding("", port)
			return nil
		})
	}
}

func (a *app) setFunnel(hp ipn.HostPort, link string, on bool) func(context.Context) error {
	return func(ctx context.Context) error {
		if on && !confirm("Make "+link+" public on the internet with Funnel?", "Make Public") {
			return nil
		}
		host, port, ok := splitHostPort(hp)
		if !ok {
			return fmt.Errorf("bad serve address %q", hp)
		}
		return a.editServe(ctx, func(sc *ipn.ServeConfig) error {
			sc.SetFunnel(host, port, on)
			return nil
		})
	}
}

func (a *app) send(to tailcfg.StableNodeID, toName string) func(context.Context) error {
	return func(ctx context.Context) (err error) {
		path, err := pickFile("Send a file to " + toName)
		if err != nil || path == "" {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("reading the file to send: %w", err)
		}
		defer func() { err = errors.Join(err, f.Close()) }()
		fi, err := f.Stat()
		if err != nil {
			return fmt.Errorf("reading the file to send: %w", err)
		}
		if err := a.lc.PushFile(ctx, to, fi.Size(), filepath.Base(path), f); err != nil {
			return fmt.Errorf("sending %s: %w", filepath.Base(path), err)
		}
		notify("Sent "+filepath.Base(path), "to "+toName)
		return nil
	}
}
