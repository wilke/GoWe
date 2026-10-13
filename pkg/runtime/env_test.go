package runtime

import (
	"context"
	"strings"
	"testing"
)

func TestBare_EnvAllowlist(t *testing.T) {
	const sentinel = "sentinel-token-value-4242"
	tests := []struct {
		name         string
		inherit      bool
		wantSentinel bool
	}{
		{"allow-list hides host secrets", false, false},
		{"inherit restores host env", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BVBRC_TOKEN", sentinel)
			res, err := NewBare().Run(context.Background(), RunSpec{
				Command:        []string{"env"},
				WorkDir:        t.TempDir(),
				Env:            map[string]string{"SPEC_VAR": "sv"},
				InheritHostEnv: tt.inherit,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(res.Stdout, sentinel); got != tt.wantSentinel {
				t.Errorf("sentinel present = %v, want %v\n%s", got, tt.wantSentinel, res.Stdout)
			}
			for _, want := range []string{"PATH=", "SPEC_VAR=sv"} {
				if !strings.Contains(res.Stdout, want) {
					t.Errorf("env missing %q\n%s", want, res.Stdout)
				}
			}
		})
	}
}

func TestApptainerRuntime_IsolationFlags(t *testing.T) {
	tests := []struct {
		name         string
		noContainall bool
		want         []string
		notWant      string
	}{
		{"default", false, []string{"exec", "--containall", "--cleanenv"}, ""},
		{"no containall", true, []string{"exec", "--cleanenv"}, "--containall"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &capture{}
			_, err := NewApptainerWithRunner(r).Run(context.Background(), RunSpec{
				Image: "alpine", Command: []string{"true"}, WorkDir: t.TempDir(),
				ApptainerNoContainall: tt.noContainall,
			})
			if err != nil {
				t.Fatal(err)
			}
			for i, w := range tt.want {
				if r.args[i] != w {
					t.Errorf("args[%d] = %q, want %q (%v)", i, r.args[i], w, r.args)
				}
			}
			if tt.notWant != "" {
				for _, a := range r.args {
					if a == tt.notWant {
						t.Errorf("unexpected %s in %v", tt.notWant, r.args)
					}
				}
			}
		})
	}
}

type capture struct{ args []string }

func (c *capture) Run(_ context.Context, _ string, args ...string) (string, string, int, error) {
	c.args = args
	return "", "", 0, nil
}
