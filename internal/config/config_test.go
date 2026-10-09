package config

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

var (
	entryA = Entry{User: "root", Host: "alpha", Key: "/keys/a.key"}
	entryB = Entry{User: "admin", Host: "beta", Key: ""}
)

// sampleTree is Prod{EU{entryA}, entryB} plus a top-level Dev folder.
func sampleTree() Config {
	return Config{Saved: Folder{Folders: []Folder{
		{Name: "Prod", Entries: []Entry{entryB}, Folders: []Folder{
			{Name: "EU", Entries: []Entry{entryA}},
		}},
		{Name: "Dev"},
	}}}
}

func TestFolderOperations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		op        func(c *Config) error
		wantErr   error
		wantPaths [][]string // every folder path afterwards; nil skips the check
		check     func(t *testing.T, c *Config)
	}{
		{
			name:      "creates a nested folder at any depth",
			op:        func(c *Config) error { return c.CreateFolder([]string{"Prod", "EU"}, "  Paris ") },
			wantPaths: [][]string{{"Prod"}, {"Prod", "EU"}, {"Prod", "EU", "Paris"}, {"Dev"}},
		},
		{
			name:    "rejects a duplicate sibling name",
			op:      func(c *Config) error { return c.CreateFolder(nil, "Prod") },
			wantErr: ErrFolderExists,
		},
		{
			name:    "rejects an empty name",
			op:      func(c *Config) error { return c.CreateFolder(nil, "   ") },
			wantErr: ErrInvalidName,
		},
		{
			name:    "rejects a name containing a slash",
			op:      func(c *Config) error { return c.CreateFolder(nil, "a/b") },
			wantErr: ErrInvalidName,
		},
		{
			name:    "rejects a missing parent",
			op:      func(c *Config) error { return c.CreateFolder([]string{"Nope"}, "x") },
			wantErr: ErrFolderNotFound,
		},
		{
			name:      "renames a folder",
			op:        func(c *Config) error { return c.RenameFolder([]string{"Prod", "EU"}, "Europe") },
			wantPaths: [][]string{{"Prod"}, {"Prod", "Europe"}, {"Dev"}},
		},
		{
			name:    "rename rejects a sibling's name",
			op:      func(c *Config) error { return c.RenameFolder([]string{"Dev"}, "Prod") },
			wantErr: ErrFolderExists,
		},
		{
			name:    "rename rejects the top level",
			op:      func(c *Config) error { return c.RenameFolder(nil, "x") },
			wantErr: ErrRootFolder,
		},
		{
			name:      "deletes a folder with its contents",
			op:        func(c *Config) error { return c.DeleteFolder([]string{"Prod"}) },
			wantPaths: [][]string{{"Dev"}},
		},
		{
			name:    "delete rejects a missing folder",
			op:      func(c *Config) error { return c.DeleteFolder([]string{"Nope"}) },
			wantErr: ErrFolderNotFound,
		},
		{
			name:    "rejects a duplicate entry in the same folder",
			op:      func(c *Config) error { return c.AddEntry([]string{"Prod"}, entryB) },
			wantErr: ErrEntryExists,
		},
		{
			name: "adds an entry",
			op:   func(c *Config) error { return c.AddEntry([]string{"Dev"}, entryA) },
			check: func(t *testing.T, c *Config) {
				dev, _ := c.Saved.Find([]string{"Dev"})
				if len(dev.Entries) != 1 || dev.Entries[0] != entryA {
					t.Errorf("Dev entries = %v", dev.Entries)
				}
			},
		},
		{
			name: "removes an entry",
			op:   func(c *Config) error { return c.RemoveEntry([]string{"Prod", "EU"}, entryA) },
			check: func(t *testing.T, c *Config) {
				if got := c.Saved.Count(); got != 1 {
					t.Errorf("Count() = %d, want 1", got)
				}
			},
		},
		{
			name:    "remove rejects an unknown entry",
			op:      func(c *Config) error { return c.RemoveEntry([]string{"Dev"}, entryA) },
			wantErr: ErrEntryNotFound,
		},
		{
			name: "moves an entry to another folder",
			op:   func(c *Config) error { return c.MoveEntry([]string{"Prod", "EU"}, entryA, []string{"Dev"}) },
			check: func(t *testing.T, c *Config) {
				eu, _ := c.Saved.Find([]string{"Prod", "EU"})
				dev, _ := c.Saved.Find([]string{"Dev"})
				if len(eu.Entries) != 0 || len(dev.Entries) != 1 {
					t.Errorf("EU = %v, Dev = %v", eu.Entries, dev.Entries)
				}
			},
		},
		{
			name: "moving an entry onto a duplicate leaves both folders unchanged",
			op: func(c *Config) error {
				if err := c.AddEntry([]string{"Dev"}, entryA); err != nil {
					return err
				}
				return c.MoveEntry([]string{"Prod", "EU"}, entryA, []string{"Dev"})
			},
			wantErr: ErrEntryExists,
			check: func(t *testing.T, c *Config) {
				if got := c.Saved.Count(); got != 3 {
					t.Errorf("Count() = %d, want 3", got)
				}
			},
		},
		{
			name:      "moves a folder into another folder",
			op:        func(c *Config) error { return c.MoveFolder([]string{"Dev"}, []string{"Prod", "EU"}) },
			wantPaths: [][]string{{"Prod"}, {"Prod", "EU"}, {"Prod", "EU", "Dev"}},
		},
		{
			name:      "moves a folder to the top level",
			op:        func(c *Config) error { return c.MoveFolder([]string{"Prod", "EU"}, nil) },
			wantPaths: [][]string{{"Prod"}, {"Dev"}, {"EU"}},
		},
		{
			name:    "rejects moving a folder into its own descendant",
			op:      func(c *Config) error { return c.MoveFolder([]string{"Prod"}, []string{"Prod", "EU"}) },
			wantErr: ErrMoveIntoSelf,
		},
		{
			name:    "rejects moving a folder into itself",
			op:      func(c *Config) error { return c.MoveFolder([]string{"Prod"}, []string{"Prod"}) },
			wantErr: ErrMoveIntoSelf,
		},
		{
			name: "rejects moving onto a sibling of the same name",
			op: func(c *Config) error {
				if err := c.CreateFolder([]string{"Dev"}, "EU"); err != nil {
					return err
				}
				return c.MoveFolder([]string{"Prod", "EU"}, []string{"Dev"})
			},
			wantErr: ErrFolderExists,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c := sampleTree()
			err := tc.op(&c)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantPaths != nil {
				if got := c.Saved.Paths(); !sameSet(got, tc.wantPaths) {
					t.Errorf("paths = %v, want %v", got, tc.wantPaths)
				}
			}
			if tc.check != nil {
				tc.check(t, &c)
			}
		})
	}
}

// sameSet compares folder paths ignoring order.
func sameSet(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, p := range a {
		found := false
		for _, q := range b {
			if reflect.DeepEqual(p, q) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestConfigJSON(t *testing.T) {
	t.Parallel()

	t.Run("round-trips saved folders and a keyless entry", func(t *testing.T) {
		t.Parallel()

		in := sampleTree()
		in.Recents = []Recent{{User: "pw", Host: "gamma", Key: ""}}
		data, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		var out Config
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(in, out) {
			t.Errorf("round trip changed the config:\n in  %+v\n out %+v", in, out)
		}
		prod, _ := out.Saved.Find([]string{"Prod"})
		if prod.Entries[0].Key != "" || out.Recents[0].Key != "" {
			t.Error("keyless connections did not keep an empty key")
		}
	})

	t.Run("loads an old config without saved", func(t *testing.T) {
		t.Parallel()

		old := `{"key_dir":"/k","recents":[{"user":"a","host":"b","key":"/k/x.key","last_used":"2024-01-01T00:00:00Z"}]}`
		var c Config
		if err := json.Unmarshal([]byte(old), &c); err != nil {
			t.Fatal(err)
		}
		if len(c.Recents) != 1 || c.Saved.Count() != 0 || len(c.Saved.Folders) != 0 {
			t.Errorf("unexpected config: %+v", c)
		}
	})
}
