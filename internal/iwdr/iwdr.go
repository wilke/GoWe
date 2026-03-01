// Package iwdr implements CWL InitialWorkDirRequirement staging logic.
// This package is used by both cwl-runner and the distributed worker.
package iwdr

import (
	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/pkg/cwl"
)

// ContainerMount represents a file/directory to mount at a specific absolute path inside a container.
// Used when InitialWorkDirRequirement has an absolute entryname path.
type ContainerMount struct {
	HostPath      string // Absolute path on the host
	ContainerPath string // Absolute path inside the container
	IsDirectory   bool   // True if this is a directory mount
}

// StageOptions configures staging behavior.
type StageOptions struct {
	CopyForContainer bool     // true for Docker/Apptainer (copy files instead of symlink)
	CWLDir           string   // CWL file directory for relative paths
	ExpressionLib    []string // JS from InlineJavascriptRequirement
	InplaceUpdate    bool     // true if InplaceUpdateRequirement is enabled (don't copy writable files)
}

// StageResult contains staging results.
type StageResult struct {
	ContainerMounts []ContainerMount      // Mounts for absolute entryname paths
	StagedPaths     map[string]string     // Maps original path -> staged path
}

// Stage processes InitialWorkDirRequirement for a CWL CommandLineTool.
// It stages files from the requirement's listing into the work directory.
// When copyForContainer is true, files are copied instead of symlinked (for Docker/Apptainer).
// Returns a StageResult with container mounts for absolute entryname paths.
func Stage(tool *cwl.CommandLineTool, inputs map[string]any, workDir string, opts StageOptions) (*StageResult, error) {
	reqRaw, ok := tool.Requirements["InitialWorkDirRequirement"]
	if !ok {
		return nil, nil
	}

	reqMap, ok := reqRaw.(map[string]any)
	if !ok {
		return nil, nil
	}

	listingRaw, ok := reqMap["listing"]
	if !ok {
		return nil, nil
	}

	evaluator := cwlexpr.NewEvaluator(opts.ExpressionLib)

	// stagedPaths maps original absolute path -> staged absolute path for entryname renames.
	stagedPaths := make(map[string]string)

	// containerMounts collects files with absolute entrynames for container mounting.
	var containerMounts []ContainerMount

	// Check if absolute entrynames are allowed. Per CWL spec, absolute paths are only
	// permitted when DockerRequirement is in the requirements section (not hints).
	allowAbsoluteEntryname := HasDockerRequirement(tool)

	// The listing can itself be an expression (e.g., "$(inputs.indir.listing)").
	listing, err := resolveIWDListing(listingRaw, inputs, evaluator)
	if err != nil {
		return nil, err
	}

	for _, item := range listing {
		if item == nil {
			continue
		}
		mounts, err := stageIWDItem(item, inputs, workDir, evaluator, opts.CWLDir, stagedPaths, opts.CopyForContainer, opts.InplaceUpdate, allowAbsoluteEntryname)
		if err != nil {
			return nil, err
		}
		containerMounts = append(containerMounts, mounts...)
	}

	return &StageResult{
		ContainerMounts: containerMounts,
		StagedPaths:     stagedPaths,
	}, nil
}

// UpdateInputPaths updates input File/Directory objects' paths to reflect staged locations.
// Per CWL spec, when a File is staged (possibly with entryname), inputs.file.path should
// point to the new location in the working directory.
func UpdateInputPaths(inputs map[string]any, workDir string, stagedPaths map[string]string) {
	for _, v := range inputs {
		updateInputPathValue(v, workDir, stagedPaths)
	}
}

// HasDockerRequirement checks if DockerRequirement is in the requirements section
// (not hints). Per CWL spec, absolute entryname paths in InitialWorkDirRequirement
// are only allowed when DockerRequirement is a requirement, not just a hint.
func HasDockerRequirement(tool *cwl.CommandLineTool) bool {
	if tool.Requirements != nil {
		if _, ok := tool.Requirements["DockerRequirement"]; ok {
			return true
		}
	}
	return false
}
