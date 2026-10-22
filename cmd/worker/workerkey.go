package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// workerKeyEnv names the environment variable that is REFUSED as a key source.
// A value in the worker's environment stays readable in /proc/<pid>/environ for
// the worker's whole lifetime by same-uid host processes, and tools without a
// DockerRequirement run on the host as the worker uid on every worker, so no
// runtime choice can hide it.
const workerKeyEnv = "GOWE_WORKER_KEY"

// errWorkerKeyOnArgv is returned when --worker-key is given a value. There is
// deliberately no override: the key would be readable in /proc/<pid>/cmdline by
// every local process, including tool containers and --runtime none tools.
var errWorkerKeyOnArgv = errors.New("--worker-key must not be passed on the command line: " +
	"the key would be readable in /proc/<pid>/cmdline by every local process; " +
	"use --worker-key-file <path> (regular file, mode 0600)")

// errWorkerKeyInEnv is returned when GOWE_WORKER_KEY is set in the environment.
var errWorkerKeyInEnv = errors.New(workerKeyEnv + " must not be set: the value stays readable in " +
	"/proc/<pid>/environ for the worker's lifetime by same-uid host processes; " +
	"unset it and pass the key with --worker-key-file <path> (regular file, mode 0600)")

// resolveWorkerKey reads the worker key from the key file. Any use of
// --worker-key (argvKeySet, even with an empty value, so a start script with an
// unset variable fails loudly) and any non-empty GOWE_WORKER_KEY in the
// environment (whether or not a key file is also given) is refused. getenv is
// injected for testing.
func resolveWorkerKey(argvKeySet bool, keyFile string, getenv func(string) string) (string, error) {
	if argvKeySet {
		return "", errWorkerKeyOnArgv
	}
	if strings.TrimSpace(getenv(workerKeyEnv)) != "" {
		return "", errWorkerKeyInEnv
	}
	if keyFile == "" {
		return "", nil
	}
	return readWorkerKeyFile(keyFile)
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
