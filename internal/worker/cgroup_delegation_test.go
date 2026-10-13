package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeControllers(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.controllers"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectCgroupLimits(t *testing.T) {
	const uid = 3581
	const all = "cpuset cpu io memory hugetlb pids rdma misc\n"
	svc := filepath.Join("user.slice", "user-3581.slice", "user@3581.service")

	tests := []struct {
		name       string
		tree       map[string]string // relative dir -> cgroup.controllers ("" key = root)
		uid        int
		mode       string
		wantCPU    bool
		wantMem    bool
		wantWarn   []string // substrings expected across warnings
		wantNoWarn bool
		wantErr    bool
	}{
		{name: "root uid uses root controllers", tree: map[string]string{"": all}, uid: 0, mode: "auto", wantCPU: true, wantMem: true, wantNoWarn: true},
		{name: "user@ delegates memory+pids", tree: map[string]string{"": all, svc: "memory pids\n", filepath.Join(svc, "user.slice"): "memory pids\n"},
			uid: uid, mode: "auto", wantMem: true, wantWarn: []string{"cpu limit not enforced", "runtime.cores"}},
		{name: "user@ delegates cpu+memory", tree: map[string]string{"": all, svc: "cpu memory pids\n", filepath.Join(svc, "user.slice"): "cpu memory pids\n"},
			uid: uid, mode: "", wantCPU: true, wantMem: true, wantNoWarn: true},
		{name: "user.slice missing falls back to user@ service", tree: map[string]string{"": all, svc: "cpu memory\n"},
			uid: uid, mode: "auto", wantCPU: true, wantMem: true, wantNoWarn: true},
		{name: "no user@ service: no limits + warn", tree: map[string]string{"": all},
			uid: uid, mode: "auto", wantWarn: []string{"no user@3581.service cgroup", "session-7.scope"}},
		{name: "cgroups v1 (no root controllers file)", tree: map[string]string{}, uid: uid, mode: "auto", wantNoWarn: true},
		{name: "override off", tree: map[string]string{"": all}, uid: 0, mode: "off", wantNoWarn: true},
		{name: "override on ignores delegation", tree: map[string]string{"": all, svc: "memory pids\n"}, uid: uid, mode: "on", wantCPU: true, wantMem: true, wantNoWarn: true},
		{name: "invalid mode", tree: map[string]string{"": all}, uid: uid, mode: "maybe", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for dir, content := range tt.tree {
				writeControllers(t, filepath.Join(root, dir), content)
			}
			got, err := detectCgroupLimits(root, tt.uid, tt.mode, "0::/user.slice/user-3581.slice/session-7.scope\n")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.CPU != tt.wantCPU || got.Memory != tt.wantMem {
				t.Errorf("cpu=%v memory=%v, want cpu=%v memory=%v", got.CPU, got.Memory, tt.wantCPU, tt.wantMem)
			}
			joined := strings.Join(got.Warnings, "\n")
			if tt.wantNoWarn && joined != "" {
				t.Errorf("unexpected warnings: %s", joined)
			}
			for _, w := range tt.wantWarn {
				if !strings.Contains(joined, w) {
					t.Errorf("warnings %q missing %q", joined, w)
				}
			}
		})
	}
}
