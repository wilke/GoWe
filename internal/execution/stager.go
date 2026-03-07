package execution

import (
	"github.com/me/gowe/pkg/staging"
)

// Stager is a type alias for the shared staging.Stager interface.
type Stager = staging.Stager

// StageOptions is a type alias for staging.StageOptions.
type StageOptions = staging.StageOptions

// StageMode is a type alias for staging.StageMode.
type StageMode = staging.StageMode

// StageMode constants.
const (
	StageModeCopy      = staging.StageModeCopy
	StageModeSymlink   = staging.StageModeSymlink
	StageModeReference = staging.StageModeReference
)

// FileStager is a type alias for the shared staging.FileStager.
type FileStager = staging.FileStager

// CompositeStager is a type alias for the shared staging.CompositeStager.
type CompositeStager = staging.CompositeStager

// NewFileStager creates a FileStager with the given stage-out mode.
func NewFileStager(mode string) *staging.FileStager {
	return staging.NewFileStager(mode)
}

// NewCompositeStager creates a CompositeStager with scheme handlers.
func NewCompositeStager(handlers map[string]Stager, fallback Stager) *staging.CompositeStager {
	return staging.NewCompositeStager(handlers, fallback)
}
