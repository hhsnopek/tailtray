package main

import (
	"slices"
	"testing"
)

func TestPortOf(t *testing.T) {
	tests := map[string]struct {
		addr   string
		want   uint16
		wantOK bool
	}{
		"wildcard":          {addr: "*:3000", want: 3000, wantOK: true},
		"loopback ipv4":     {addr: "127.0.0.1:7681", want: 7681, wantOK: true},
		"bracketed ipv6":    {addr: "[::1]:3000", want: 3000, wantOK: true},
		"any address":       {addr: "0.0.0.0:22", want: 22, wantOK: true},
		"no port":           {addr: "localhost"},
		"port zero":         {addr: "*:0"},
		"port out of range": {addr: "*:70000"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, ok := portOf(tc.addr)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("portOf(%q) = %d, %v; want %d, %v", tc.addr, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestParseListeners(t *testing.T) {
	tests := map[string]struct {
		parse func(string) []listener
		out   string
		want  []listener
	}{
		"lsof, two processes": {
			parse: parseLsof,
			out:   "p501\ncnode\nf21\nn*:3000\nf22\nn[::1]:3000\np777\ncttyd\nf9\nn127.0.0.1:7681\n",
			want:  []listener{{3000, 501, "node"}, {3000, 501, "node"}, {7681, 777, "ttyd"}},
		},
		"lsof, nothing listening": {
			parse: parseLsof,
			out:   "",
		},
		"ss, own and foreign processes": {
			parse: parseSS,
			out: "LISTEN 0 511 127.0.0.1:3000 0.0.0.0:* users:((\"node\",pid=1714,fd=21))\n" +
				"LISTEN 0 4096 0.0.0.0:22 0.0.0.0:*\n" +
				"LISTEN 0 128 [::]:8080 [::]:* users:((\"python3\",pid=99,fd=3))\n",
			want: []listener{{3000, 1714, "node"}, {8080, 99, "python3"}},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := tc.parse(tc.out); !slices.Equal(got, tc.want) {
				t.Errorf("got %v; want %v", got, tc.want)
			}
		})
	}
}

func TestHasTTY(t *testing.T) {
	out := "  501 ttys003\n  777 ??\n 1714 pts/1\n   99 ?\nbogus\n"
	got := hasTTY(out)
	for pid, want := range map[int]bool{501: true, 777: false, 1714: true, 99: false} {
		if got[pid] != want {
			t.Errorf("hasTTY()[%d] = %v; want %v", pid, got[pid], want)
		}
	}
}
