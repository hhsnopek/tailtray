package main

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/tailcfg/nodecap"
)

// entry is one menu item: a separator, a label (no do), an action, or a
// submenu (sub).
type entry struct {
	title    string
	toggle   bool // shows a checkmark when on
	on       bool
	disabled bool
	dot      dot
	sep      bool
	sub      []entry
	do       func(context.Context) error
}

type dot int

const (
	noDot dot = iota
	onlineDot
	offlineDot
)

var separator = entry{sep: true}

const adminURL = "https://login.tailscale.com/admin/machines"

// expiryWarn is how far ahead the menu starts warning about the node key.
const expiryWarn = 7 * 24 * time.Hour

// same reports whether a and b render identically, so an unchanged state
// does not rebuild a menu that may be open.
func same(a, b []entry) bool {
	return slices.EqualFunc(a, b, func(x, y entry) bool {
		return x.title == y.title && x.toggle == y.toggle && x.on == y.on &&
			x.disabled == y.disabled && x.dot == y.dot && x.sep == y.sep &&
			(x.do == nil) == (y.do == nil) && same(x.sub, y.sub)
	})
}

// state is everything the menu shows, read from tailscaled.
type state struct {
	status    *ipnstate.Status
	prefs     *ipn.Prefs
	profile   ipn.LoginProfile
	profiles  []ipn.LoginProfile
	serve     *ipn.ServeConfig
	targets   []apitype.FileTarget
	suggest   tailcfg.StableNodeID
	listeners []listener
	readonly  bool
}

func (s state) running() bool {
	return s.status != nil && s.status.BackendState == ipn.Running.String()
}

func (s state) dnsSuffix() string {
	if s.status == nil || s.status.CurrentTailnet == nil {
		return ""
	}
	return s.status.CurrentTailnet.MagicDNSSuffix
}

func (a *app) entries(s state) []entry {
	var es []entry
	if s.status != nil {
		for _, h := range s.status.Health {
			es = append(es, entry{title: "⚠ " + h, disabled: true})
		}
		if w := expiry(s.status.Self, time.Now()); w != "" {
			es = append(es, entry{title: "⚠ " + w, do: a.open(adminURL)})
		}
	}
	if s.readonly {
		fix := "sudo tailscale set --operator=$USER"
		es = append(es,
			entry{title: "No permission to manage Tailscale", disabled: true},
			entry{title: "Copy the Fix: " + fix, do: a.copy(fix)},
			separator)
	}
	quit := entry{title: "Quit Tailtray", do: a.quit}
	if s.status == nil {
		return append(es, entry{title: "Tailscale Isn't Running", disabled: true}, separator, quit)
	}

	switch s.status.BackendState {
	case ipn.Running.String():
		es = append(es, entry{title: "Connected", toggle: true, on: true, disabled: s.readonly, do: a.edit(&ipn.MaskedPrefs{Prefs: ipn.Prefs{WantRunning: false}, WantRunningSet: true})})
	case ipn.Starting.String():
		es = append(es, entry{title: "Connecting…", disabled: true})
	case ipn.NeedsLogin.String():
		es = append(es, entry{title: "Log In…", disabled: s.readonly, do: a.login})
	case ipn.NeedsMachineAuth.String():
		es = append(es, entry{title: "Waiting for an Admin to Approve This Device", disabled: true})
	default:
		es = append(es, entry{title: "Connected", toggle: true, disabled: s.readonly, do: a.edit(&ipn.MaskedPrefs{Prefs: ipn.Prefs{WantRunning: true}, WantRunningSet: true})})
	}
	if !s.running() {
		return append(es, separator, a.settings(s), a.account(s), separator, quit)
	}

	self := s.status.Self
	es = append(es, entry{title: fmt.Sprintf("This Device: %s (%s)", name(self), firstIP(self)), do: a.copy(firstIP(self))}, separator)
	if t, ok := a.exitToggle(s); ok {
		es = append(es, t)
	}
	es = append(es, a.exitNodes(s), a.devices(s), a.serving(s))
	if len(s.targets) > 0 {
		es = append(es, a.sendFile(s))
	}
	return append(es, separator, a.settings(s), a.account(s), separator, quit)
}

// expiry returns a warning when self's key expires within expiryWarn of now.
// Tagged nodes, and keys with expiry disabled, have no KeyExpiry.
func expiry(self *ipnstate.PeerStatus, now time.Time) string {
	if self == nil || self.KeyExpiry == nil {
		return ""
	}
	left := self.KeyExpiry.Sub(now)
	switch {
	case left <= 0:
		return "Key Expired"
	case left > expiryWarn:
		return ""
	case left < 48*time.Hour:
		return "Key Expires in " + plural(max(1, int(left/time.Hour)), "Hour")
	}
	return "Key Expires in " + plural(int(left/(24*time.Hour)), "Day")
}

// exitToggle returns an entry that turns the exit node in use, or the last
// one used, on and off in one click.
func (a *app) exitToggle(s state) (entry, bool) {
	var id tailcfg.StableNodeID
	on := false
	switch {
	case s.status.ExitNodeStatus != nil && s.status.ExitNodeStatus.ID != "":
		id, on = s.status.ExitNodeStatus.ID, true
	case s.prefs != nil:
		id = s.prefs.InternalExitNodePrior
	}
	p := peer(s.status, id)
	if p == nil {
		return entry{}, false
	}
	return entry{title: "Exit Node: " + name(p), toggle: true, on: on, disabled: s.readonly || (!on && !p.Online), do: a.useExitNode(!on)}, true
}

func (a *app) exitNodes(s state) entry {
	var cur tailcfg.StableNodeID
	if ens := s.status.ExitNodeStatus; ens != nil {
		cur = ens.ID
	}
	var nodes []*ipnstate.PeerStatus
	for _, p := range s.status.Peer {
		if p.ExitNodeOption && p.Location == nil {
			nodes = append(nodes, p)
		}
	}
	slices.SortFunc(nodes, byName)

	sub := []entry{{title: "None", toggle: true, on: cur == "", disabled: s.readonly || cur == "", do: a.useExitNode(false)}}
	if len(nodes) > 0 {
		sub = append(sub, separator)
	}
	for _, p := range nodes {
		t := name(p)
		if p.ID == s.suggest {
			t += " (Recommended)"
		}
		sub = append(sub, entry{title: t, toggle: true, on: p.ID == cur, disabled: s.readonly || !p.Online, dot: dotFor(p), do: a.setExitNode(p.ID)})
	}
	lan := s.prefs != nil && s.prefs.ExitNodeAllowLANAccess
	sub = append(sub, separator, entry{title: "Allow Local Network Access", toggle: true, on: lan, disabled: s.readonly, do: a.edit(&ipn.MaskedPrefs{Prefs: ipn.Prefs{ExitNodeAllowLANAccess: !lan}, ExitNodeAllowLANAccessSet: true})})
	return entry{title: "Exit Nodes", sub: sub}
}

func (a *app) devices(s state) entry {
	var mine, tagged, shared []*ipnstate.PeerStatus
	for _, p := range s.status.Peer {
		switch {
		case p.Tags != nil && p.Tags.Len() > 0:
			tagged = append(tagged, p)
		case p.UserID == s.status.Self.UserID:
			mine = append(mine, p)
		default:
			shared = append(shared, p)
		}
	}
	var sub []entry
	for _, g := range []struct {
		title string
		peers []*ipnstate.PeerStatus
	}{{"My Devices", mine}, {"Tagged Devices", tagged}, {"Shared With Me", shared}} {
		if len(g.peers) == 0 {
			continue
		}
		if len(sub) > 0 {
			sub = append(sub, separator)
		}
		sub = append(sub, entry{title: g.title, disabled: true})
		slices.SortFunc(g.peers, byName)
		for _, p := range g.peers {
			sub = append(sub, a.device(p, s.dnsSuffix()))
		}
	}
	if len(sub) == 0 {
		sub = append(sub, entry{title: "No Other Devices", disabled: true})
	}
	return entry{title: "Devices", sub: sub}
}

func (a *app) device(p *ipnstate.PeerStatus, suffix string) entry {
	var sub []entry
	if p.Online {
		sub = append(sub, entry{title: path(p), disabled: true})
		if len(p.TailscaleIPs) > 0 {
			sub = append(sub, entry{title: "Ping", do: a.ping(name(p), p.TailscaleIPs[0])})
		}
		if len(p.SSH_HostKeys) > 0 {
			sub = append(sub, entry{title: "SSH…", do: a.openSSH(name(p))})
		}
		sub = append(sub, separator)
	}
	for _, ip := range p.TailscaleIPs {
		sub = append(sub, entry{title: "Copy " + ip.String(), do: a.copy(ip.String())})
	}
	if dns := strings.TrimSuffix(p.DNSName, "."); dns != "" && strings.HasSuffix(dns, suffix) {
		sub = append(sub, entry{title: "Copy " + dns, do: a.copy(dns)})
	}
	if !p.Online && !p.LastSeen.IsZero() {
		sub = append(sub, separator, entry{title: "Last Seen " + ago(time.Since(p.LastSeen)), disabled: true})
	}
	return entry{title: name(p), dot: dotFor(p), sub: sub}
}

// path describes how traffic to p travels.
func path(p *ipnstate.PeerStatus) string {
	switch {
	case p.CurAddr != "":
		return "Direct: " + p.CurAddr
	case p.PeerRelay != "":
		return "Peer Relay: " + p.PeerRelay
	case p.Active && p.Relay != "":
		return "Relayed via " + p.Relay
	}
	return "No Recent Traffic"
}

// funnelPorts are the HTTPS ports Funnel accepts by default, so a port
// served from the menu can be made public later.
var funnelPorts = []uint16{443, 8443, 10000}

// maxListeners caps the running ports offered for serving.
const maxListeners = 8

func (a *app) serving(s state) entry {
	host := strings.TrimSuffix(s.status.Self.DNSName, ".")
	suffix := s.dnsSuffix()
	served := map[uint16]bool{}
	var sub []entry
	if sc := s.serve; sc != nil {
		for _, hp := range slices.Sorted(maps.Keys(sc.Web)) {
			h, port, ok := splitHostPort(hp)
			if !ok {
				continue
			}
			public := sc.AllowFunnel[hp]
			var paths []entry
			for _, mount := range slices.Sorted(maps.Keys(sc.Web[hp].Handlers)) {
				hh := sc.Web[hp].Handlers[mount]
				if p, ok := proxyPort(hh.Proxy); ok {
					served[p] = true
				}
				link := serveURL(h, port, mount)
				paths = append(paths, entry{title: mount + " → " + describe(hh), sub: []entry{
					{title: "Open", do: a.open(link)},
					{title: "Copy URL", do: a.copy(link)},
					separator,
					{title: "Stop Serving", disabled: s.readonly, do: a.stopServing(h, port, mount)},
				}})
			}
			if funnelAllowed(s.status.Self, port) {
				paths = append(paths, separator, entry{title: "Public with Funnel", toggle: true, on: public, disabled: s.readonly, do: a.setFunnel(hp, serveURL(h, port, "/"), !public)})
			}
			t := fmt.Sprintf("Port %d", port)
			if public {
				t += " (Public)"
			}
			sub = append(sub, entry{title: t, sub: paths})
		}
		for _, port := range slices.Sorted(maps.Keys(sc.TCP)) {
			fwd := sc.TCP[port].TCPForward
			if fwd == "" {
				continue
			}
			if p, ok := proxyPort(fwd); ok {
				served[p] = true
			}
			sub = append(sub, entry{title: fmt.Sprintf("Port %d TCP → %s", port, fwd), sub: []entry{
				{title: "Stop Serving", disabled: s.readonly, do: a.stopForwarding(port)},
			}})
		}
	}
	if len(sub) > 0 {
		sub = append(sub, separator)
	}
	var running []entry
	for _, l := range s.listeners {
		if served[l.port] || len(running) == maxListeners {
			continue
		}
		running = append(running, entry{title: fmt.Sprintf("Serve Port %d (%s)", l.port, l.process), disabled: s.readonly, do: a.servePort(host, suffix, l.port)})
	}
	if len(running) > 0 {
		sub = append(sub, entry{title: "Running Locally", disabled: true})
		sub = append(sub, running...)
		sub = append(sub, separator)
	}
	sub = append(sub, entry{title: "Serve a Local Port…", disabled: s.readonly, do: a.serveLocal(host, suffix)})
	return entry{title: "Serve", sub: sub}
}

func (a *app) sendFile(s state) entry {
	targets := slices.Clone(s.targets)
	slices.SortFunc(targets, func(x, y apitype.FileTarget) int {
		return cmp.Compare(short(x.Node.Name), short(y.Node.Name))
	})
	var sub []entry
	for _, t := range targets {
		n := short(t.Node.Name)
		p := peer(s.status, t.Node.StableID)
		on := p != nil && p.Online
		d := offlineDot
		if on {
			d = onlineDot
		}
		sub = append(sub, entry{title: n + "…", dot: d, disabled: !on, do: a.send(t.Node.StableID, n)})
	}
	return entry{title: "Send File", sub: sub}
}

func (a *app) settings(s state) entry {
	routes := s.prefs != nil && s.prefs.RouteAll
	dns := s.prefs != nil && s.prefs.CorpDNS
	off := s.readonly || s.prefs == nil
	return entry{title: "Settings", sub: []entry{
		{title: "Accept Routes", toggle: true, on: routes, disabled: off, do: a.edit(&ipn.MaskedPrefs{Prefs: ipn.Prefs{RouteAll: !routes}, RouteAllSet: true})},
		{title: "Use Tailscale DNS", toggle: true, on: dns, disabled: off, do: a.edit(&ipn.MaskedPrefs{Prefs: ipn.Prefs{CorpDNS: !dns}, CorpDNSSet: true})},
	}}
}

func (a *app) account(s state) entry {
	var sub, others []entry
	for _, p := range s.profiles {
		current := p.ID == s.profile.ID
		sub = append(sub, entry{title: profileName(p), toggle: true, on: current, disabled: s.readonly || current, do: a.switchProfile(p.ID)})
		if !current {
			others = append(others, entry{title: profileName(p) + "…", disabled: s.readonly, do: a.removeProfile(p.ID, profileName(p))})
		}
	}
	if len(sub) > 0 {
		sub = append(sub, separator)
	}
	sub = append(sub, entry{title: "Add Account…", disabled: s.readonly, do: a.addAccount(profileName(s.profile))})
	if len(others) > 0 {
		sub = append(sub, entry{title: "Remove Account", sub: others})
	}
	if s.profile.ID != "" {
		sub = append(sub, entry{title: "Log Out…", disabled: s.readonly, do: a.logout(profileName(s.profile))})
	}
	sub = append(sub, separator, entry{title: "Admin Console…", do: a.open(adminURL)})
	return entry{title: "Account: " + profileName(s.profile), sub: sub}
}

func profileName(p ipn.LoginProfile) string {
	tailnet := p.NetworkProfile.DisplayNameOrDefault()
	if p.Name == "" {
		return "None"
	}
	if tailnet == "" || strings.EqualFold(tailnet, p.Name) {
		return p.Name
	}
	return p.Name + " (" + tailnet + ")"
}

func peer(st *ipnstate.Status, id tailcfg.StableNodeID) *ipnstate.PeerStatus {
	if id == "" {
		return nil
	}
	for _, p := range st.Peer {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// name is a device's MagicDNS label, the name the tailnet resolves.
func name(p *ipnstate.PeerStatus) string {
	if n := short(p.DNSName); n != "" {
		return n
	}
	return p.HostName
}

func short(fqdn string) string {
	n, _, _ := strings.Cut(fqdn, ".")
	return n
}

func byName(x, y *ipnstate.PeerStatus) int {
	return cmp.Compare(strings.ToLower(name(x)), strings.ToLower(name(y)))
}

func firstIP(p *ipnstate.PeerStatus) string {
	if len(p.TailscaleIPs) == 0 {
		return "no address"
	}
	return p.TailscaleIPs[0].String()
}

func dotFor(p *ipnstate.PeerStatus) dot {
	if p.Online {
		return onlineDot
	}
	return offlineDot
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "Just Now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "Minute") + " Ago"
	case d < 48*time.Hour:
		return plural(int(d/time.Hour), "Hour") + " Ago"
	}
	return plural(int(d/(24*time.Hour)), "Day") + " Ago"
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

func describe(h *ipn.HTTPHandler) string {
	switch {
	case h.Proxy != "":
		u := strings.TrimPrefix(strings.TrimPrefix(h.Proxy, "http://"), "https+insecure://")
		return strings.TrimSuffix(u, "/")
	case h.Path != "":
		return h.Path
	case h.Text != "":
		return "Text"
	}
	return "Nothing"
}

// proxyPort is the local port a serve target points at, from forms such as
// http://localhost:3000/, 127.0.0.1:5432 and 3000.
func proxyPort(target string) (uint16, bool) {
	if target == "" {
		return 0, false
	}
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		target = u.Host
	}
	if _, p, err := net.SplitHostPort(target); err == nil {
		target = p
	}
	p, err := strconv.ParseUint(strings.TrimSuffix(target, "/"), 10, 16)
	if err != nil {
		return 0, false
	}
	return uint16(p), true
}

func serveURL(host string, port uint16, mount string) string {
	if port == 443 {
		return "https://" + host + mount
	}
	return fmt.Sprintf("https://%s:%d%s", host, port, mount)
}

func splitHostPort(hp ipn.HostPort) (string, uint16, bool) {
	h, p, err := net.SplitHostPort(string(hp))
	if err != nil {
		return "", 0, false
	}
	port, err := strconv.ParseUint(p, 10, 16)
	if err != nil {
		return "", 0, false
	}
	return h, uint16(port), true
}

// funnelAllowed reports whether the tailnet lets this device use Funnel on
// port, from capabilities such as
// https://tailscale.com/cap/funnel-ports?ports=443,8443,10000-10010.
func funnelAllowed(self *ipnstate.PeerStatus, port uint16) bool {
	if !self.CapMap.Contains(nodecap.Funnel) {
		return false
	}
	for c := range self.CapMap {
		base, query, ok := strings.Cut(string(c), "?")
		if !ok || base != string(nodecap.FunnelPorts) {
			continue
		}
		q, err := url.ParseQuery(query)
		if err != nil {
			continue
		}
		for r := range strings.SplitSeq(q.Get("ports"), ",") {
			lo, hi, _ := strings.Cut(r, "-")
			if hi == "" {
				hi = lo
			}
			l, err1 := strconv.ParseUint(lo, 10, 16)
			h, err2 := strconv.ParseUint(hi, 10, 16)
			if err1 == nil && err2 == nil && uint64(port) >= l && uint64(port) <= h {
				return true
			}
		}
	}
	return false
}
