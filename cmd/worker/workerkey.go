package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// workerKeyEnv is the environment variable that carries the worker key. It is
// read once at startup and removed from the process environment so that tool
// processes the worker spawns do not inherit it. That is NOT protection against
// reading it: os.Unsetenv only edits Go's private copy, and the kernel keeps
// the exec-time environment block, which /proc/<pid>/environ exposes for the
// worker's whole lifetime to same-uid processes sharing the host PID
// namespace. Prefer --worker-key-file; see checkWorkerKeySource.
const workerKeyEnv = "GOWE_WORKER_KEY"

// errWorkerKeyOnArgv is returned when --worker-key is given a value. There is
// deliberately no override: the key would be readable in /proc/<pid>/cmdline by
// every local process, including tool containers and --runtime none tools.
var errWorkerKeyOnArgv = errors.New("--worker-key must not be passed on the command line: " +
	"the key would be readable in /proc/<pid>/cmdline by every local process; " +
	"use --worker-key-file <path> (regular file, mode 0600) or the " + workerKeyEnv + " environment variable")

// checkWorkerKeySource decides whether a key sourced from the environment is
// acceptable. fromEnv is true when the key came from GOWE_WORKER_KEY. runtimes
// is the comma-separated --runtime value ("none" anywhere means tools run
// directly on the host with no PID namespace); noContainall is
// --apptainer-no-containall. It returns warn=true when the env form is used
// but allowed, and an error when it must be refused. The key file has no such
// residue, so a file source never warns or refuses.
func checkWorkerKeySource(fromEnv bool, runtimes string, noContainall bool) (warn bool, err error) {
	if !fromEnv {
		return false, nil
	}
	for _, rt := range strings.Split(runtimes, ",") {
		if strings.TrimSpace(rt) == "none" {
			return false, fmt.Errorf("%s is refused with --runtime %q: tools run on the host without a PID namespace and could read the key from /proc/<pid>/environ; use --worker-key-file", workerKeyEnv, runtimes)
		}
	}
	if noContainall {
		return false, fmt.Errorf("%s is refused with --apptainer-no-containall: Apptainer tools share the host PID namespace and could read the key from /proc/<pid>/environ; use --worker-key-file", workerKeyEnv)
	}
	return true, nil
}

// workerKeyFromEnv reports whether the resolved key came from the environment:
// no key file was given and the key is non-empty.
func workerKeyFromEnv(keyFile, key string) bool {
	return keyFile == "" && key != ""
}

// resolveWorkerKey determines the worker key from, in order of precedence, the
// key file and the GOWE_WORKER_KEY environment variable. Any use of --worker-key (argvKeySet, even with an
// empty value, so a start script with an unset variable fails loudly) is
// refused. getenv/unsetenv are injected for testing.
func resolveWorkerKey(argvKeySet bool, keyFile string, getenv func(string) string, unsetenv func(string) error) (string, error) {
	if argvKeySet {
		return "", errWorkerKeyOnArgv
	}

	// Always take the env var out of the process environment, whichever
	// source wins.
	envKey := strings.TrimSpace(getenv(workerKeyEnv))
	if err := unsetenv(workerKeyEnv); err != nil {
		return "", fmt.Errorf("unset %s: %w", workerKeyEnv, err)
	}

	if keyFile != "" {
		key, err := readWorkerKeyFile(keyFile)
		if err != nil {
			return "", err
		}
		return key, nil
	}
	return envKey, nil
}

// checkKeyFileNotBound refuses a key file located under any path that is
// bound into tool containers or used as the task work/image directory: tools
// run as the worker uid and could read it. Paths are made absolute with
// symlinks resolved before comparing.
func checkKeyFileNotBound(keyFile string, bound []string) error {
	key := canonicalPath(keyFile)
	for _, b := range bound {
		if b == "" {
			continue
		}
		cb := canonicalPath(b)
		if key == cb || strings.HasPrefix(key, strings.TrimRight(cb, string(os.PathSeparator))+string(os.PathSeparator)) {
			return fmt.Errorf("worker key file %s is under %s, which tool processes can read; move the key file outside --workdir, --image-dir, --pre-stage-dir, --extra-bind and --dataset paths", keyFile, b)
		}
	}
	return nil
}

func canonicalPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	return abs
}

// readWorkerKeyFile reads the worker key from path. The path must be a regular
// file that is not accessible to group or others (mode 0600 or stricter).
func readWorkerKeyFile(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("worker key file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("worker key file %s is not a regular file", path)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return "", fmt.Errorf("worker key file %s has mode %04o; it must be 0600 or stricter (chmod 600 %s)", path, perm, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read worker key file: %w", err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", fmt.Errorf("worker key file %s is empty", path)
	}
	return key, nil
}
