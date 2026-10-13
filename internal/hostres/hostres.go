// Package hostres holds the host-resource flag parsing and resolution shared
// by gowe-worker and cwl-runner: the repeatable/comma-separated flag types,
// the NAME=value env-file parser, --env/--env-file merging, and the
// --pre-stage-dir / --dataset / --extra-bind resolution into container bind
// mounts.
//
// Since Apptainer tools run with --containall --cleanenv, explicit bind mounts
// and env vars are the only way host databases, model weights and settings
// reach a container.
package hostres

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/me/gowe/internal/toolexec"
)

// StringSliceFlag implements flag.Value (and pflag.Value) to allow repeated
// flags like --extra-bind /a --extra-bind /b. Each value is also split on
// commas, so --extra-bind /a,/b works too.
type StringSliceFlag struct {
	Values *[]string
}

func (f *StringSliceFlag) String() string {
	if f == nil || f.Values == nil {
		return ""
	}
	return strings.Join(*f.Values, ",")
}

func (f *StringSliceFlag) Set(val string) error {
	for _, p := range strings.Split(val, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			*f.Values = append(*f.Values, p)
		}
	}
	return nil
}

// Type implements pflag.Value.
func (f *StringSliceFlag) Type() string { return "strings" }

// StringMapFlag implements flag.Value (and pflag.Value) to allow repeated
// flags like --dataset id1=path1 --dataset id2=path2. Each value is also
// split on commas, so --dataset id1=path1,id2=path2 works too. It is also
// used for --env NAME=value.
type StringMapFlag struct {
	Values *map[string]string
}

func (f *StringMapFlag) String() string {
	if f == nil || f.Values == nil {
		return ""
	}
	var parts []string
	for k, v := range *f.Values {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

func (f *StringMapFlag) Set(val string) error {
	if *f.Values == nil {
		*f.Values = make(map[string]string)
	}
	for _, rawPair := range strings.Split(val, ",") {
		pair := strings.TrimSpace(rawPair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid dataset %q, expected id=path", pair)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" || value == "" {
			return fmt.Errorf("invalid dataset %q, id and path must be non-empty", pair)
		}
		(*f.Values)[key] = value
	}
	return nil
}

// Type implements pflag.Value.
func (f *StringMapFlag) Type() string { return "strings" }

// ParseEnvFile reads a file with NAME=value lines. Lines starting with # and
// blank lines are skipped; lines without '=' are ignored.
func ParseEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key != "" {
			out[key] = value
		}
	}
	return out, nil
}

// MergeEnv combines an --env-file with --env flags. File entries are loaded
// first; --env flags override file entries with the same name. Returns nil
// when nothing is set. Any name in reserved is refused.
func MergeEnv(envFile string, envFlags map[string]string, reserved ...string) (map[string]string, error) {
	merged := make(map[string]string)
	if envFile != "" {
		loaded, err := ParseEnvFile(envFile)
		if err != nil {
			return nil, fmt.Errorf("load env file: %w", err)
		}
		for k, v := range loaded {
			merged[k] = v
		}
	}
	for k, v := range envFlags {
		merged[k] = v
	}
	for _, name := range reserved {
		if _, ok := merged[name]; ok {
			return nil, fmt.Errorf("%s must not be set via --env/--env-file", name)
		}
	}
	if len(merged) == 0 {
		return nil, nil
	}
	return merged, nil
}

// ResolveBinds turns --pre-stage-dir, --dataset and --extra-bind into the
// dataset map (id -> absolute path) and the container bind mounts (each at the
// same path inside the container, read-write). The whole pre-stage dir is
// bound, with each subdirectory registered as a dataset.
func ResolveBinds(logger *slog.Logger, preStageDir string, datasetAliases map[string]string, extraBindPaths []string) (map[string]string, []toolexec.ExtraBind, error) {
	datasets := make(map[string]string)
	var extraBinds []toolexec.ExtraBind

	if preStageDir != "" {
		absPreStage, err := filepath.Abs(preStageDir)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve pre-stage-dir: %w", err)
		}
		entries, err := os.ReadDir(absPreStage)
		if err != nil {
			return nil, nil, fmt.Errorf("scan pre-stage-dir %s: %w", absPreStage, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				datasets[entry.Name()] = filepath.Join(absPreStage, entry.Name())
			}
		}
		extraBinds = append(extraBinds, toolexec.ExtraBind{
			HostPath:      absPreStage,
			ContainerPath: absPreStage,
		})
		logger.Info("pre-stage-dir scanned", "path", absPreStage, "datasets", len(datasets))
	}

	// Explicit dataset aliases are additive.
	for id, path := range datasetAliases {
		absPath, err := filepath.Abs(path)
		if err != nil {
			logger.Warn("resolve dataset path", "id", id, "path", path, "error", err)
			absPath = path
		}
		datasets[id] = absPath
	}

	// Generic pass-through binds.
	for _, p := range extraBindPaths {
		absP, err := filepath.Abs(p)
		if err != nil {
			logger.Warn("resolve extra-bind path", "path", p, "error", err)
			absP = p
		}
		extraBinds = append(extraBinds, toolexec.ExtraBind{
			HostPath:      absP,
			ContainerPath: absP,
		})
	}
	return datasets, extraBinds, nil
}
