package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var hostIDHexRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

func TestDeriveHostID_FallbackOrder(t *testing.T) {
	const (
		machineID = "4c4c4544003a3110804ab4c04f4e4d33"
		bootID    = "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d"
		hostname  = "compute-01"
	)
	tests := []struct {
		name     string
		files    map[string]string // file name -> content; absent = missing file
		hostname string
		source   string // raw value expected to be hashed; "" = no id
	}{
		{"machine-id present", map[string]string{"machine-id": machineID + "\n", "boot_id": bootID + "\n"}, hostname, machineID},
		{"empty machine-id falls back to boot_id", map[string]string{"machine-id": "\n", "boot_id": bootID + "\n"}, hostname, bootID},
		{"missing machine-id falls back to boot_id", map[string]string{"boot_id": bootID}, hostname, bootID},
		{"both missing falls back to hostname", nil, hostname, hostname},
		{"both empty falls back to hostname", map[string]string{"machine-id": "", "boot_id": " \n"}, hostname, hostname},
		{"nothing available", nil, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tt.files {
				writeFile(t, filepath.Join(dir, name), content)
			}
			machinePath, bootPath := filepath.Join(dir, "machine-id"), filepath.Join(dir, "boot_id")

			got := deriveHostID(machinePath, bootPath, tt.hostname)
			if tt.source == "" {
				if got != "" {
					t.Errorf("host id = %q, want empty", got)
				}
				return
			}
			sum := sha256.Sum256([]byte(tt.source))
			if want := hex.EncodeToString(sum[:])[:16]; got != want {
				t.Errorf("host id = %q, want %q (hash of %q)", got, want, tt.source)
			}
			if !hostIDHexRe.MatchString(got) {
				t.Errorf("host id = %q, want 16 lowercase hex chars", got)
			}
			if got == tt.source || strings.Contains(tt.source, got) {
				t.Errorf("host id %q exposes the raw input", got)
			}
			if again := deriveHostID(machinePath, bootPath, tt.hostname); again != got {
				t.Errorf("host id not stable: %q then %q", got, again)
			}
		})
	}
}

func TestResolveHostID(t *testing.T) {
	dir := t.TempDir()
	machinePath := filepath.Join(dir, "machine-id")
	writeFile(t, machinePath, "abc\n")
	bootPath := filepath.Join(dir, "boot_id") // missing

	tests := []struct {
		name     string
		override string
		want     string
		wantErr  bool
	}{
		{"no override derives", "", hashHostID("abc"), false},
		{"valid override used verbatim", "node-01.rack_A", "node-01.rack_A", false},
		{"max length", strings.Repeat("a", 64), strings.Repeat("a", 64), false},
		{"too long", strings.Repeat("a", 65), "", true},
		{"space", "node 01", "", true},
		{"slash", "a/b", "", true},
		{"colon", "a:b", "", true},
		{"non-ascii", "höst", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveHostID(tt.override, machinePath, bootPath, "h")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("host id = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseCPUList(t *testing.T) {
	tests := []struct {
		list string
		want int
	}{
		{"0-383\n", 384},
		{"0", 1},
		{"0-3,8-11,16", 9},
		{" 0-1 , 4 ", 3},
		{"", 0},
		{"abc", 0},
		{"3-1", 0},
		{"0-", 0},
	}
	for _, tt := range tests {
		t.Run(tt.list, func(t *testing.T) {
			if got := parseCPUList(tt.list); got != tt.want {
				t.Errorf("parseCPUList(%q) = %d, want %d", tt.list, got, tt.want)
			}
		})
	}
}

func TestHostCPUCount(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "online")
	if err := os.WriteFile(good, []byte("0-15\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("junk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := hostCPUCount(good, 4); got != 16 {
		t.Errorf("online file: got %d, want 16", got)
	}
	if got := hostCPUCount(bad, 4); got != 4 {
		t.Errorf("unparsable file: got %d, want fallback 4", got)
	}
	if got := hostCPUCount(filepath.Join(dir, "missing"), 4); got != 4 {
		t.Errorf("missing file: got %d, want fallback 4", got)
	}
}

func TestDeriveHostID_SkipsUninitializedMachineID(t *testing.T) {
	dir := t.TempDir()
	machine := filepath.Join(dir, "machine-id")
	boot := filepath.Join(dir, "boot_id")
	if err := os.WriteFile(machine, []byte("uninitialized\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(boot, []byte("boot-1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := deriveHostID(machine, boot, "h"), hashHostID("boot-1234"); got != want {
		t.Errorf("deriveHostID = %q, want the boot_id hash %q", got, want)
	}
}
