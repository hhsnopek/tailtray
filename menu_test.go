package main

import (
	"context"
	"net/netip"
	"slices"
	"testing"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/tailcfg/nodecap"
	"tailscale.com/types/key"
)

func TestProxyPort(t *testing.T) {
	tests := map[string]struct {
		target string
		want   uint16
		wantOK bool
	}{
		"url with path":     {target: "http://localhost:3000/", want: 3000, wantOK: true},
		"insecure https":    {target: "https+insecure://localhost:8443", want: 8443, wantOK: true},
		"host and port":     {target: "127.0.0.1:5432", want: 5432, wantOK: true},
		"bare port":         {target: "3000", want: 3000, wantOK: true},
		"empty":             {target: ""},
		"unix socket":       {target: "unix:/tmp/app.sock"},
		"url without port":  {target: "http://localhost/"},
		"port out of range": {target: "localhost:70000"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, ok := proxyPort(tc.target)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("proxyPort(%q) = %d, %v; want %d, %v", tc.target, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestFunnelAllowed(t *testing.T) {
	ports := nodecap.FunnelPorts + "?ports=443,8443,10000-10010"
	tests := map[string]struct {
		caps tailcfg.NodeCapMap
		port uint16
		want bool
	}{
		"listed port":            {caps: tailcfg.NodeCapMap{nodecap.Funnel: nil, ports: nil}, port: 8443, want: true},
		"port inside a range":    {caps: tailcfg.NodeCapMap{nodecap.Funnel: nil, ports: nil}, port: 10005, want: true},
		"port outside":           {caps: tailcfg.NodeCapMap{nodecap.Funnel: nil, ports: nil}, port: 8080},
		"no funnel attribute":    {caps: tailcfg.NodeCapMap{ports: nil}, port: 443},
		"no ports capability":    {caps: tailcfg.NodeCapMap{nodecap.Funnel: nil}, port: 443},
		"no capabilities at all": {port: 443},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := funnelAllowed(&ipnstate.PeerStatus{CapMap: tc.caps}, tc.port); got != tc.want {
				t.Errorf("funnelAllowed(%d) = %v; want %v", tc.port, got, tc.want)
			}
		})
	}
}

func TestExpiry(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time {
		e := now.Add(d)
		return &e
	}
	tests := map[string]struct {
		expires *time.Time
		want    string
	}{
		"expiry disabled": {},
		"weeks away":      {expires: at(10 * 24 * time.Hour)},
		"days away":       {expires: at(3*24*time.Hour + time.Hour), want: "Key Expires in 3 Days"},
		"under two days":  {expires: at(30 * time.Hour), want: "Key Expires in 30 Hours"},
		"under an hour":   {expires: at(30 * time.Minute), want: "Key Expires in 1 Hour"},
		"already expired": {expires: at(-time.Minute), want: "Key Expired"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := expiry(&ipnstate.PeerStatus{KeyExpiry: tc.expires}, now); got != tc.want {
				t.Errorf("expiry() = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestPath(t *testing.T) {
	tests := map[string]struct {
		peer ipnstate.PeerStatus
		want string
	}{
		"direct":           {peer: ipnstate.PeerStatus{CurAddr: "192.168.1.1:41641", Relay: "ord", Active: true}, want: "Direct: 192.168.1.1:41641"},
		"peer relay":       {peer: ipnstate.PeerStatus{PeerRelay: "100.1.2.3:7777:1", Active: true}, want: "Peer Relay: 100.1.2.3:7777:1"},
		"derp":             {peer: ipnstate.PeerStatus{Relay: "ord", Active: true}, want: "Relayed via ord"},
		"idle with a home": {peer: ipnstate.PeerStatus{Relay: "ord"}, want: "No Recent Traffic"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := path(&tc.peer); got != tc.want {
				t.Errorf("path() = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestAgo(t *testing.T) {
	tests := map[string]struct {
		d    time.Duration
		want string
	}{
		"seconds":  {d: 20 * time.Second, want: "Just Now"},
		"a minute": {d: time.Minute, want: "1 Minute Ago"},
		"hours":    {d: 5 * time.Hour, want: "5 Hours Ago"},
		"one day":  {d: 47 * time.Hour, want: "47 Hours Ago"},
		"days":     {d: 206 * 24 * time.Hour, want: "206 Days Ago"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := ago(tc.d); got != tc.want {
				t.Errorf("ago(%v) = %q; want %q", tc.d, got, tc.want)
			}
		})
	}
}

func TestSame(t *testing.T) {
	act := func(context.Context) error { return nil }
	base := []entry{{title: "Devices", sub: []entry{{title: "dpi", dot: onlineDot}}}, separator, {title: "Quit", do: act}}
	tests := map[string]struct {
		other []entry
		want  bool
	}{
		"identical":                   {other: []entry{{title: "Devices", sub: []entry{{title: "dpi", dot: onlineDot}}}, separator, {title: "Quit", do: act}}, want: true},
		"another closure, same shape": {other: []entry{{title: "Devices", sub: []entry{{title: "dpi", dot: onlineDot}}}, separator, {title: "Quit", do: func(context.Context) error { return nil }}}, want: true},
		"submenu device went offline": {other: []entry{{title: "Devices", sub: []entry{{title: "dpi", dot: offlineDot}}}, separator, {title: "Quit", do: act}}},
		"action became a label":       {other: []entry{{title: "Devices", sub: []entry{{title: "dpi", dot: onlineDot}}}, separator, {title: "Quit"}}},
		"item removed":                {other: base[:2]},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := same(base, tc.other); got != tc.want {
				t.Errorf("same() = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestEntriesTopLevel(t *testing.T) {
	a, err := newApp(&local.Client{}, dark)
	if err != nil {
		t.Fatal(err)
	}
	router := &ipnstate.PeerStatus{ID: "n1", DNSName: "router.example.ts.net.", Online: true, ExitNodeOption: true, TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.1")}}
	self := &ipnstate.PeerStatus{ID: "n0", DNSName: "mac.example.ts.net.", TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.2")}}
	running := func(exit *ipnstate.ExitNodeStatus) *ipnstate.Status {
		return &ipnstate.Status{
			BackendState:   ipn.Running.String(),
			Self:           self,
			Peer:           map[key.NodePublic]*ipnstate.PeerStatus{key.NewNode().Public(): router},
			ExitNodeStatus: exit,
			CurrentTailnet: &ipnstate.TailnetStatus{MagicDNSSuffix: "example.ts.net"},
		}
	}
	tests := map[string]struct {
		s    state
		want []string
	}{
		"daemon unreachable": {
			want: []string{"Tailscale Isn't Running", "", "Quit Tailtray"},
		},
		"stopped": {
			s:    state{status: &ipnstate.Status{BackendState: ipn.Stopped.String()}},
			want: []string{"Connected", "", "Settings", "Account: None", "", "Quit Tailtray"},
		},
		"needs login": {
			s:    state{status: &ipnstate.Status{BackendState: ipn.NeedsLogin.String()}},
			want: []string{"Log In…", "", "Settings", "Account: None", "", "Quit Tailtray"},
		},
		"running, no exit node used yet": {
			s:    state{status: running(nil), prefs: &ipn.Prefs{}},
			want: []string{"Connected", "This Device: mac (100.64.0.2)", "", "Exit Nodes", "Devices", "Serve", "", "Settings", "Account: None", "", "Quit Tailtray"},
		},
		"running through an exit node": {
			s:    state{status: running(&ipnstate.ExitNodeStatus{ID: "n1", Online: true}), prefs: &ipn.Prefs{}},
			want: []string{"Connected", "This Device: mac (100.64.0.2)", "", "Exit Node: router", "Exit Nodes", "Devices", "Serve", "", "Settings", "Account: None", "", "Quit Tailtray"},
		},
		"exit node off, last one remembered": {
			s:    state{status: running(nil), prefs: &ipn.Prefs{InternalExitNodePrior: "n1"}},
			want: []string{"Connected", "This Device: mac (100.64.0.2)", "", "Exit Node: router", "Exit Nodes", "Devices", "Serve", "", "Settings", "Account: None", "", "Quit Tailtray"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, e := range a.entries(tc.s) {
				got = append(got, e.title)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("titles:\n got  %q\n want %q", got, tc.want)
			}
		})
	}
}
