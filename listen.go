package main

import (
	"bufio"
	"cmp"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// listener is a local TCP port a process of this user listens on.
type listener struct {
	port    uint16
	pid     int
	process string
}

// parseLsof reads `lsof -nP -iTCP -sTCP:LISTEN -Fpcn`: p and c lines name
// each process, and an n line follows for each address it listens on.
func parseLsof(out string) []listener {
	var (
		ls      []listener
		pid     int
		process string
	)
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			// A pid that fails to parse stays 0, which interactive drops.
			pid, _ = strconv.Atoi(line[1:])
		case 'c':
			process = line[1:]
		case 'n':
			if p, ok := portOf(line[1:]); ok {
				ls = append(ls, listener{port: p, pid: pid, process: process})
			}
		}
	}
	return ls
}

var ssProcess = regexp.MustCompile(`users:\(\("([^"]+)",pid=(\d+)`)

// parseSS reads `ss -Hltnp`. Without root, ss names only this user's
// processes, which are the ones worth serving.
func parseSS(out string) []listener {
	var ls []listener
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 {
			continue
		}
		m := ssProcess.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		pid, _ := strconv.Atoi(m[2]) // ssProcess admits only digits
		if p, ok := portOf(f[3]); ok {
			ls = append(ls, listener{port: p, pid: pid, process: m[1]})
		}
	}
	return ls
}

// portOf is the port of an address such as *:3000, 127.0.0.1:7681,
// [::1]:3000 or 0.0.0.0:22.
func portOf(addr string) (uint16, bool) {
	i := strings.LastIndexByte(addr, ':')
	if i < 0 {
		return 0, false
	}
	p, err := strconv.ParseUint(addr[i+1:], 10, 16)
	if err != nil || p == 0 {
		return 0, false
	}
	return uint16(p), true
}

// interactive returns one listener per port, sorted, for the processes that
// run in a terminal, which is what sets a dev server apart from an app's
// background helper. A process usually listens on IPv4 and IPv6 both.
func interactive(ls []listener) []listener {
	if len(ls) == 0 {
		return nil
	}
	slices.SortFunc(ls, func(x, y listener) int { return cmp.Compare(x.port, y.port) })
	ls = slices.CompactFunc(ls, func(x, y listener) bool { return x.port == y.port })
	pids := make([]string, len(ls))
	for i, l := range ls {
		pids[i] = strconv.Itoa(l.pid)
	}
	// ps exits 1 when a process ended since the scan, but still lists the
	// rest, so its output counts whatever the exit status.
	out, _ := exec.Command("ps", "-o", "pid=,tty=", "-p", strings.Join(pids, ",")).Output()
	tty := hasTTY(string(out))
	return slices.DeleteFunc(ls, func(l listener) bool { return !tty[l.pid] })
}

// hasTTY reads `ps -o pid=,tty=`, where a process without a terminal shows
// ?? on macOS and ? on Linux.
func hasTTY(out string) map[int]bool {
	m := map[int]bool{}
	for line := range strings.Lines(out) {
		f := strings.Fields(line)
		if len(f) != 2 || strings.Trim(f[1], "?") == "" {
			continue
		}
		if pid, err := strconv.Atoi(f[0]); err == nil {
			m[pid] = true
		}
	}
	return m
}
