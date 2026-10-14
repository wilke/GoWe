package hostres

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestStringSliceFlag(t *testing.T) {
	tests := []struct {
		name string
		sets []string
		want []string
	}{
		{"single", []string{"/a"}, []string{"/a"}},
		{"repeated", []string{"/a", "/b"}, []string{"/a", "/b"}},
		{"comma", []string{"/a,/b"}, []string{"/a", "/b"}},
		{"mixed with spaces and empties", []string{" /a , ,/b", "/c"}, []string{"/a", "/b", "/c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			f := &StringSliceFlag{Values: &got}
			for _, s := range tt.sets {
				if err := f.Set(s); err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestStringMapFlag(t *testing.T) {
	tests := []struct {
		name    string
		sets    []string
		want    map[string]string
		wantErr bool
	}{
		{"single", []string{"a=/x"}, map[string]string{"a": "/x"}, false},
		{"repeated", []string{"a=/x", "b=/y"}, map[string]string{"a": "/x", "b": "/y"}, false},
		{"comma", []string{"a=/x,b=/y"}, map[string]string{"a": "/x", "b": "/y"}, false},
		{"value with equals", []string{"A=b=c"}, map[string]string{"A": "b=c"}, false},
		{"later wins", []string{"a=1", "a=2"}, map[string]string{"a": "2"}, false},
		{"missing equals", []string{"a"}, nil, true},
		{"empty value", []string{"a="}, nil, true},
		{"empty key", []string{"=x"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got map[string]string
			f := &StringMapFlag{Values: &got}
			var err error
			for _, s := range tt.sets {
				if err = f.Set(s); err != nil {
					break
				}
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v want %v", got, tt.want)
			}
		})
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseEnvFile(t *testing.T) {
	p := writeFile(t, "# comment\n\nA=1\n  B = two words  \nbadline\n=novalue\nC=x=y\n   # indented comment\nEMPTY=\n")
	got, err := ParseEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "two words", "C": "x=y", "EMPTY": ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
	if _, err := ParseEnvFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestMergeEnv(t *testing.T) {
	file := writeFile(t, "# c\nA=file\nB=file\n\n")
	tests := []struct {
		name     string
		file     string
		flags    map[string]string
		reserved []string
		want     map[string]string
		wantErr  bool
	}{
		{"nothing", "", nil, nil, nil, false},
		{"flags only", "", map[string]string{"X": "1"}, nil, map[string]string{"X": "1"}, false},
		{"file only", file, nil, nil, map[string]string{"A": "file", "B": "file"}, false},
		{"flag overrides file", file, map[string]string{"A": "flag"}, nil, map[string]string{"A": "flag", "B": "file"}, false},
		{"reserved via flag", "", map[string]string{"HOME": "/x"}, []string{"HOME", "TMPDIR"}, nil, true},
		{"reserved via file", writeFile(t, "TMPDIR=/x\n"), nil, []string{"HOME", "TMPDIR"}, nil, true},
		{"missing file", filepath.Join(t.TempDir(), "nope"), nil, nil, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MergeEnv(tt.file, tt.flags, tt.reserved...)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestResolveBinds(t *testing.T) {
	pre := t.TempDir()
	for _, d := range []string{"boltz", "chai"} {
		if err := os.Mkdir(filepath.Join(pre, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(pre, "file.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	extra := t.TempDir()

	ds, binds, err := ResolveBinds(quietLogger(), pre, map[string]string{"alias": "/opt/x"}, []string{extra})
	if err != nil {
		t.Fatal(err)
	}
	wantDS := map[string]string{
		"boltz": filepath.Join(pre, "boltz"),
		"chai":  filepath.Join(pre, "chai"),
		"alias": "/opt/x",
	}
	if !reflect.DeepEqual(ds, wantDS) {
		t.Errorf("datasets = %v want %v", ds, wantDS)
	}
	if len(binds) != 2 {
		t.Fatalf("binds = %v", binds)
	}
	for i, p := range []string{pre, extra} {
		if binds[i].HostPath != p || binds[i].ContainerPath != p {
			t.Errorf("bind %d = %+v, want same-path %s", i, binds[i], p)
		}
	}

	if _, _, err := ResolveBinds(quietLogger(), filepath.Join(pre, "missing"), nil, nil); err == nil {
		t.Error("expected error for missing pre-stage-dir")
	}
	ds, binds, err = ResolveBinds(quietLogger(), "", nil, nil)
	if err != nil || len(ds) != 0 || len(binds) != 0 {
		t.Errorf("empty: %v %v %v", ds, binds, err)
	}
}
