package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sync"
)

// sshTargets remembers the user@host last used to SSH to each device, so the
// next SSH to it asks with that answer filled in.
type sshTargets struct {
	mu sync.Mutex
}

func (t *sshTargets) get(device string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if dest := t.load()[device]; dest != "" {
		return dest
	}
	u, err := user.Current()
	if err != nil {
		return device
	}
	return u.Username + "@" + device
}

func (t *sshTargets) set(device, dest string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	path, err := sshFile()
	if err != nil {
		return err
	}
	m := t.load()
	m[device] = dest
	b, err := json.MarshalIndent(m, "", "\t")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("remembering %s: %w", dest, err)
	}
	if err := os.WriteFile(path+".tmp", b, 0o600); err != nil {
		return fmt.Errorf("remembering %s: %w", dest, err)
	}
	return os.Rename(path+".tmp", path)
}

// load returns an empty map when nothing has been saved yet or the file is
// unreadable, since a forgotten answer only costs retyping it.
func (t *sshTargets) load() map[string]string {
	m := map[string]string{}
	path, err := sshFile()
	if err != nil {
		return m
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]string{}
	}
	return m
}

func sshFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tailtray", "ssh.json"), nil
}
