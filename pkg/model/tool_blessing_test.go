package model

import (
	"strings"
	"testing"
	"time"
)

func TestToolBlessing_ActiveAllowsGroupAllowsImage(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	b := &ToolBlessing{WorkerGroups: []string{"anlseq"}, ImageSHA256s: []string{sha}}
	if !b.Active() {
		t.Error("new blessing is active")
	}
	now := time.Now()
	b.RevokedAt = &now
	if b.Active() {
		t.Error("revoked blessing is not active")
	}
	var nilB *ToolBlessing
	if nilB.Active() || nilB.AllowsGroup("x") || nilB.AllowsImage(sha) {
		t.Error("nil blessing allows nothing")
	}

	t.Run("groups", func(t *testing.T) {
		if !b.AllowsGroup("anlseq") || b.AllowsGroup("default") || b.AllowsGroup("") {
			t.Error("restricted to anlseq")
		}
		open := &ToolBlessing{}
		if !open.AllowsGroup("anything") || !open.AllowsGroup("") {
			t.Error("empty list means any group")
		}
		def := &ToolBlessing{WorkerGroups: []string{"default"}}
		if !def.AllowsGroup("") {
			t.Error("empty group is the default group")
		}
	})
	t.Run("images", func(t *testing.T) {
		if !b.AllowsImage(sha) || !b.AllowsImage("sha256:"+strings.ToUpper(sha)) {
			t.Error("digest match should ignore prefix and case")
		}
		if b.AllowsImage(strings.Repeat("cd", 32)) || b.AllowsImage("") {
			t.Error("unlisted or empty digest must be refused")
		}
		if (&ToolBlessing{}).AllowsImage(sha) {
			t.Error("a blessing with no pinned images allows none")
		}
	})
	t.Run("env names", func(t *testing.T) {
		if got := (&ToolBlessing{}).EffectiveEnvNames(); strings.Join(got, ",") != "BVBRC_TOKEN,KB_AUTH_TOKEN" {
			t.Errorf("default env names: %v", got)
		}
		if got := (&ToolBlessing{EnvNames: []string{"X"}}).EffectiveEnvNames(); len(got) != 1 || got[0] != "X" {
			t.Errorf("explicit env names: %v", got)
		}
	})
}

func TestIsValidSHA256(t *testing.T) {
	good := strings.Repeat("0f", 32)
	for _, s := range []string{good, "sha256:" + good, strings.ToUpper(good)} {
		if !IsValidSHA256(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []string{"", "abc", good + "0", strings.Repeat("zz", 32)} {
		if IsValidSHA256(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestScrubSecrets_KeepsBlessingMetadata(t *testing.T) {
	h := &RuntimeHints{
		InjectBVBRCToken: true, TokenEnvNames: []string{"BVBRC_TOKEN"}, ToolHash: "sha256:x",
		TokenBlessingID: "tb_1", ImageSHA256s: []string{"ab", "cd"},
		Secrets:         map[string]string{"A": "b"},
		StagerOverrides: &StagerOverrides{HTTPCredential: &HTTPCredential{Token: "t"}},
	}
	h.ScrubSecrets()
	if h.Secrets != nil || h.StagerOverrides.HTTPCredential != nil {
		t.Error("secrets must be cleared")
	}
	if len(h.TokenEnvNames) != 1 || h.ToolHash != "sha256:x" || h.TokenBlessingID != "tb_1" || len(h.ImageSHA256s) != 2 || !h.InjectBVBRCToken {
		t.Errorf("blessing metadata must survive ScrubSecrets: %+v", h)
	}
}
