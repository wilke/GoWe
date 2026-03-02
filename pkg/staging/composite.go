package staging

import (
	"context"
	"fmt"
)

// CompositeStager routes staging operations to scheme-specific handlers.
type CompositeStager struct {
	handlers map[string]Stager
	fallback Stager
}

// NewCompositeStager creates a CompositeStager with scheme handlers.
// The handlers map routes URI schemes to specific stagers.
// The fallback stager is used for unregistered schemes and for StageOut.
func NewCompositeStager(handlers map[string]Stager, fallback Stager) *CompositeStager {
	return &CompositeStager{
		handlers: handlers,
		fallback: fallback,
	}
}

// StageIn routes to the appropriate handler based on scheme.
func (s *CompositeStager) StageIn(ctx context.Context, location string, destPath string) error {
	scheme, _ := ParseLocationScheme(location)
	if handler, ok := s.handlers[scheme]; ok {
		return handler.StageIn(ctx, location, destPath)
	}
	if s.fallback != nil {
		return s.fallback.StageIn(ctx, location, destPath)
	}
	return fmt.Errorf("no stager registered for scheme %q", scheme)
}

// StageOut uses the fallback stager (typically file-based).
func (s *CompositeStager) StageOut(ctx context.Context, srcPath string, taskID string) (string, error) {
	if s.fallback != nil {
		return s.fallback.StageOut(ctx, srcPath, taskID)
	}
	return "", fmt.Errorf("no fallback stager configured for stage-out")
}

// Verify interface compliance.
var _ Stager = (*CompositeStager)(nil)
