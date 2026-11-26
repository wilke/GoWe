package groupacl

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// A service identity (<name>@service.gowe) can be a user of a group but never
// its manager: refused on write (naming the entry), ignored with one WARN
// when a stored rule holds one (the rule's other fields still apply, start-up
// does not fail), and never a manager for IsManager / ManagedGroups.
func TestServiceIdentityNeverManager(t *testing.T) {
	ctx := context.Background()
	const svc = "model-router@service.gowe"
	const other = "other@service.gowe"

	t.Run("write refused, naming the entry", func(t *testing.T) {
		for _, name := range []string{svc, "Router@Service.GOWE"} {
			_, err := ValidateManagers([]string{"mgr", name})
			if err == nil || !strings.Contains(err.Error(), `"`+name+`"`) || !strings.Contains(err.Error(), "cannot be a worker group manager") {
				t.Errorf("ValidateManagers(%q) = %v, want the refusal naming it", name, err)
			}
		}
		if got, err := ValidateManagers([]string{"mgr"}); err != nil || !reflect.DeepEqual(got, []string{"mgr"}) {
			t.Errorf("ValidateManagers(mgr) = %v, %v", got, err)
		}
		// In users it stays allowed.
		if got, err := ValidateUsers([]string{svc}); err != nil || !reflect.DeepEqual(got, []string{svc}) {
			t.Errorf("ValidateUsers(%q) = %v, %v; want it allowed", svc, got, err)
		}
	})

	t.Run("stored entry ignored with one WARN", func(t *testing.T) {
		var logs bytes.Buffer
		p := New().WithAdmins(adminsOf("root"))
		p.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
		gpu := gpm("gpu", []string{svc, "mgr"}, "alice", svc)
		gpu.Admins = true
		cpu := gpm("cpu", []string{other})
		if err := p.Reload(ctx, &memSource{defaultDeny: "true", rules: []*model.WorkerGroupPolicy{gpu, cpu}}); err != nil {
			t.Fatalf("Reload must not fail for a service manager: %v", err)
		}
		for _, c := range []struct {
			user, group string
			manager     bool
			allowed     bool
		}{
			// svc is a user of gpu, not its manager; admins: true still
			// applies; other, only listed as a manager of cpu, gets nothing.
			{svc, "gpu", false, true},
			{"mgr", "gpu", true, true},
			{"alice", "gpu", false, true},
			{"root", "gpu", false, true},
			{other, "cpu", false, false},
			{"mallory", "gpu", false, false},
		} {
			if got := p.IsManager(c.user, c.group); got != c.manager {
				t.Errorf("IsManager(%q, %q) = %v, want %v", c.user, c.group, got, c.manager)
			}
			if got := p.Allowed(c.user, c.group); got != c.allowed {
				t.Errorf("Allowed(%q, %q) = %v, want %v", c.user, c.group, got, c.allowed)
			}
		}
		if got := p.ManagedGroups(svc); got != nil {
			t.Errorf("ManagedGroups(%q) = %v, want nil", svc, got)
		}
		if got := p.ManagedGroups(other); got != nil {
			t.Errorf("ManagedGroups(%q) = %v, want nil", other, got)
		}
		out := logs.String()
		if n := strings.Count(out, "level=WARN"); n != 1 {
			t.Errorf("%d WARN lines, want 1:\n%s", n, out)
		}
		for _, want := range []string{"gpu: " + svc, "cpu: " + other} {
			if !strings.Contains(out, want) {
				t.Errorf("the WARN does not name %q:\n%s", want, out)
			}
		}

		// No WARN when nothing is ignored.
		logs.Reset()
		if err := p.Reload(ctx, &memSource{rules: testRules}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(logs.String(), "level=WARN") {
			t.Errorf("WARN without a service manager:\n%s", logs.String())
		}
	})

	t.Run("on the default group: ignored, not an error", func(t *testing.T) {
		p := New()
		p.SetLogger(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
		if err := p.Reload(ctx, &memSource{rules: []*model.WorkerGroupPolicy{gpm("default", []string{svc}, "alice")}}); err != nil {
			t.Errorf("Reload = %v, want nil (the only manager is ignored)", err)
		}
		if !p.Allowed("alice", "default") || p.IsManager(svc, "default") {
			t.Error("the default rule's users must apply and the service must not manage it")
		}
	})

	t.Run("a never-loaded or nil policy has no service manager", func(t *testing.T) {
		var nilPolicy *Policy
		if nilPolicy.IsManager(svc, "gpu") || New().IsManager(svc, "gpu") {
			t.Error("IsManager is true for a service identity")
		}
	})
}
