package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateUnique(t *testing.T) {
	tests := map[string]struct {
		taken []string
		name  string
		want  string
	}{
		"free name":                       {name: "report.pdf", want: "report.pdf"},
		"taken once":                      {taken: []string{"report.pdf"}, name: "report.pdf", want: "report (2).pdf"},
		"taken twice":                     {taken: []string{"report.pdf", "report (2).pdf"}, name: "report.pdf", want: "report (3).pdf"},
		"no extension":                    {taken: []string{"README"}, name: "README", want: "README (2)"},
		"double extension keeps the last": {taken: []string{"a.tar.gz"}, name: "a.tar.gz", want: "a.tar (2).gz"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for _, n := range tc.taken {
				if err := os.WriteFile(filepath.Join(dir, n), []byte("old"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			f, err := createUnique(dir, tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if got := filepath.Base(f.Name()); got != tc.want {
				t.Errorf("created %q; want %q", got, tc.want)
			}
			for _, n := range tc.taken {
				if b, _ := os.ReadFile(filepath.Join(dir, n)); string(b) != "old" {
					t.Errorf("%s was overwritten", n)
				}
			}
		})
	}
}
