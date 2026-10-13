package runtime

import (
	"os"
	"strings"
)

// BaseHostEnv returns the allow-listed subset of the host environment that a
// tool process running directly on the host (no container boundary) may
// inherit: PATH, LANG, LC_*, TZ, USER, LOGNAME, SHELL and TERM, plus NVIDIA_*,
// CUDA_* and LD_LIBRARY_PATH when gpu is true. Everything else (provider
// tokens, cloud credentials, the worker key, ...) is withheld.
func BaseHostEnv(gpu bool) []string {
	var out []string
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if hostEnvAllowed(name, gpu) {
			out = append(out, kv)
		}
	}
	return out
}

func hostEnvAllowed(name string, gpu bool) bool {
	switch name {
	case "PATH", "LANG", "TZ", "USER", "LOGNAME", "SHELL", "TERM":
		return true
	}
	if strings.HasPrefix(name, "LC_") {
		return true
	}
	if gpu {
		if name == "LD_LIBRARY_PATH" || strings.HasPrefix(name, "NVIDIA_") || strings.HasPrefix(name, "CUDA_") {
			return true
		}
	}
	return false
}
