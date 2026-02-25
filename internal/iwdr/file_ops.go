package iwdr

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// writeIWDFile writes content to a file in the work directory.
func writeIWDFile(workDir, name, content string) error {
	outPath := filepath.Join(workDir, name)
	// Create parent directory if needed.
	if dir := filepath.Dir(outPath); dir != workDir {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create parent dir for %q: %w", name, err)
		}
	}
	return os.WriteFile(outPath, []byte(content), 0644)
}

// writeIWDFileWithMounts writes content to a file and handles absolute paths for containers.
// If name is an absolute path and copyForContainer is true, the file is written to workDir
// but a ContainerMount is returned to mount it at the absolute path inside the container.
// allowAbsoluteEntryname is validated upstream; this function trusts it.
func writeIWDFileWithMounts(workDir, name, content string, copyForContainer bool, allowAbsoluteEntryname bool) ([]ContainerMount, error) {
	// Handle absolute path for container execution.
	if copyForContainer && strings.HasPrefix(name, "/") {
		// Write to a staging file in workDir, return mount for container.
		// Use the basename for local storage.
		stageName := "_mount_" + filepath.Base(name)
		stagePath := filepath.Join(workDir, stageName)
		if err := os.WriteFile(stagePath, []byte(content), 0644); err != nil {
			return nil, fmt.Errorf("write staged file for %q: %w", name, err)
		}
		return []ContainerMount{{
			HostPath:      stagePath,
			ContainerPath: name,
			IsDirectory:   false,
		}}, nil
	}

	// Normal case: write to workDir with the given name.
	if err := writeIWDFile(workDir, name, content); err != nil {
		return nil, err
	}
	return nil, nil
}

// copyFile copies a file from src to dst with write permissions.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	return os.WriteFile(dst, data, 0644)
}

// copyDir recursively copies a directory tree with write permissions.
func copyDir(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dst, srcInfo.Mode()|0755); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// CopyDirContents copies the contents of src directory into dst directory.
// Unlike copyDir, this does not create a subdirectory with the source name.
func CopyDirContents(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}
