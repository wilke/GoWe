package worker

import (
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

func TestResolveServiceSettings(t *testing.T) {
	inv := []model.GPUInfo{{Index: 0, UUID: "GPU-a"}, {Index: 1, UUID: "GPU-b"}}
	base := Config{ServiceMode: true, Runtime: "apptainer", WorkerKey: "k"}
	tests := []struct {
		name      string
		cfg       func(c *Config)
		hostname  string
		hostID    string
		inventory []model.GPUInfo
		slots     int
		wantErr   string // substring; "" = accepted
		wantHost  string
		lo, hi    int
	}{
		{name: "defaults", hostname: "gpu01", hostID: "h1", slots: 1, wantHost: "gpu01", lo: 20000, hi: 20999},
		{name: "explicit host and range", cfg: func(c *Config) { c.AdvertiseHost = "10.0.0.5"; c.ServicePortLo, c.ServicePortHi = 31000, 31003 },
			hostname: "gpu01", hostID: "h1", slots: 4, wantHost: "10.0.0.5", lo: 31000, hi: 31003},
		{name: "gpu with uuid inventory, concurrency 1", cfg: func(c *Config) { c.GPU.Enabled = true },
			hostname: "gpu01", hostID: "h1", inventory: inv, slots: 1, wantHost: "gpu01", lo: 20000, hi: 20999},
		{name: "runtime with none", cfg: func(c *Config) { c.Runtime = "apptainer,none" }, hostname: "gpu01", hostID: "h1", slots: 1, wantErr: "--runtime apptainer"},
		{name: "no worker key", cfg: func(c *Config) { c.WorkerKey = "" }, hostname: "gpu01", hostID: "h1", slots: 1, wantErr: "worker key"},
		{name: "no host id", hostname: "gpu01", slots: 1, wantErr: "host id"},
		{name: "gpu without inventory", cfg: func(c *Config) { c.GPU.Enabled = true }, hostname: "gpu01", hostID: "h1", inventory: []model.GPUInfo{}, slots: 1, wantErr: "GPU inventory"},
		{name: "gpu without uuid", cfg: func(c *Config) { c.GPU.Enabled = true }, hostname: "gpu01", hostID: "h1", inventory: []model.GPUInfo{{Index: 3}}, slots: 1, wantErr: "GPU 3 has no UUID"},
		{name: "inherit host env", cfg: func(c *Config) { c.InheritHostEnv = true }, hostname: "gpu01", hostID: "h1", slots: 1, wantErr: "--inherit-host-env"},
		{name: "no containall", cfg: func(c *Config) { c.ApptainerNoContainall = true }, hostname: "gpu01", hostID: "h1", slots: 1, wantErr: "--apptainer-no-containall"},
		{name: "too few ports", cfg: func(c *Config) { c.ServicePortLo, c.ServicePortHi = 31000, 31001 }, hostname: "gpu01", hostID: "h1", slots: 3, wantErr: "fewer than --concurrency 3"},
		{name: "invalid advertise host", cfg: func(c *Config) { c.AdvertiseHost = "bad host" }, hostname: "gpu01", hostID: "h1", slots: 1, wantErr: "invalid --advertise-host"},
		{name: "hostname not advertisable", hostname: "my_host", hostID: "h1", slots: 1, wantErr: "set --advertise-host"},
		{name: "no hostname", hostID: "h1", slots: 1, wantErr: "set --advertise-host"},
		{name: "concurrency above the heartbeat service limit", hostname: "gpu01", hostID: "h1", slots: model.MaxHeartbeatServices + 1, wantErr: "at most --concurrency 64"},
		{name: "concurrency at the heartbeat service limit", hostname: "gpu01", hostID: "h1", slots: model.MaxHeartbeatServices, wantHost: "gpu01", lo: 20000, hi: 20999},
		{name: "service settings without service mode", cfg: func(c *Config) { *c = Config{AdvertiseHost: "gpu01"} }, slots: 1, wantErr: "require --service-mode"},
		{name: "batch mode", cfg: func(c *Config) { *c = Config{Runtime: "none", InheritHostEnv: true} }, slots: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			if tt.cfg != nil {
				tt.cfg(&cfg)
			}
			got, err := resolveServiceSettings(cfg, tt.hostname, tt.hostID, tt.inventory, tt.slots)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got.advertiseHost != tt.wantHost || got.portLo != tt.lo || got.portHi != tt.hi {
				t.Errorf("got %+v, want host %q range %d-%d", got, tt.wantHost, tt.lo, tt.hi)
			}
		})
	}
}

func TestResolveServiceSettings_LinuxOnly(t *testing.T) {
	orig := serviceModeGOOS
	serviceModeGOOS = "darwin"
	t.Cleanup(func() { serviceModeGOOS = orig })
	cfg := Config{ServiceMode: true, Runtime: "apptainer", WorkerKey: "k"}
	if _, err := resolveServiceSettings(cfg, "gpu01", "h1", nil, 1); err == nil || !strings.Contains(err.Error(), "Linux only") {
		t.Errorf("err = %v, want the Linux-only refusal", err)
	}
	// Batch mode does not care.
	if _, err := resolveServiceSettings(Config{Runtime: "none"}, "gpu01", "h1", nil, 1); err != nil {
		t.Errorf("batch: %v", err)
	}
}

// Service mode with --gpu gets a device ledger at every concurrency (each
// service gets exactly its charged devices); batch mode only above one slot.
// When the service-mode inventory check and validateGPUConcurrency both
// fail, the service-mode message is given.
func TestResolveGPUAndServiceSettings(t *testing.T) {
	svc := Config{ServiceMode: true, Runtime: "apptainer", WorkerKey: "k", GPU: GPUWorkerConfig{Enabled: true}}
	batch := Config{Runtime: "apptainer", GPU: GPUWorkerConfig{Enabled: true}}
	tests := []struct {
		name       string
		cfg        Config
		inventory  []model.GPUInfo
		slots      int
		wantLedger int    // devices of the ledger; -1 = no ledger
		wantErr    string // substring
		notErr     string // substring the error must not contain
	}{
		{name: "service mode, 1 slot, 2 GPUs", cfg: svc, inventory: fakeGPUs(2), slots: 1, wantLedger: 2},
		{name: "service mode, 2 slots, 2 GPUs", cfg: svc, inventory: fakeGPUs(2), slots: 2, wantLedger: 2},
		{name: "service mode without --gpu", cfg: Config{ServiceMode: true, Runtime: "apptainer", WorkerKey: "k"}, slots: 1, wantLedger: -1},
		{name: "batch, 1 slot keeps the static config", cfg: batch, inventory: fakeGPUs(2), slots: 1, wantLedger: -1},
		{name: "batch, 2 slots", cfg: batch, inventory: fakeGPUs(2), slots: 2, wantLedger: 2},
		{name: "batch, 2 slots, no inventory", cfg: batch, inventory: []model.GPUInfo{}, slots: 2,
			wantErr: "per-task GPU assignment needs the GPU inventory"},
		{name: "service mode, 2 slots, no inventory: the service-mode message", cfg: svc, inventory: []model.GPUInfo{}, slots: 2,
			wantErr: "--service-mode with --gpu needs the GPU inventory", notErr: "--concurrency 1"},
		{name: "service mode, 2 slots, GPU without UUID: the service-mode message", cfg: svc, inventory: []model.GPUInfo{{Index: 4}}, slots: 2,
			wantErr: "--service-mode with --gpu: GPU 4 has no UUID", notErr: "--concurrency 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ledger, err := resolveGPUAndServiceSettings(tt.cfg, "gpu01", "h1", tt.inventory, tt.slots)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
				}
				if tt.notErr != "" && strings.Contains(err.Error(), tt.notErr) {
					t.Errorf("err = %v, must not contain %q", err, tt.notErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			switch {
			case tt.wantLedger < 0 && ledger != nil:
				t.Errorf("ledger over %v, want none", ledger.devices)
			case tt.wantLedger >= 0 && (ledger == nil || ledger.size() != tt.wantLedger):
				t.Errorf("ledger = %v, want one of %d devices", ledger, tt.wantLedger)
			}
		})
	}
}

func TestClientRegister_ServiceMode(t *testing.T) {
	for _, mode := range []bool{false, true} {
		fs := newFakeServer(t, regRespOld)
		c := NewClient(fs.srv.URL, nil)
		if _, err := c.Register(t.Context(), "n", "h", "g", "apptainer", RegisterOptions{ServiceMode: mode}); err != nil {
			t.Fatal(err)
		}
		got, present := fs.registration()["service_mode"]
		if mode && got != true {
			t.Errorf("service mode: service_mode = %v, want true", got)
		}
		if !mode && present {
			t.Errorf("batch mode: service_mode sent (%v)", got)
		}
	}
}
