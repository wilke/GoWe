package staging

import (
	"context"
	"fmt"
	"strings"
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
func (s *CompositeStager) StageIn(ctx context.Context, location string, destPath string, opts StageOptions) error {
	scheme, _ := ParseLocationScheme(location)
	if handler, ok := s.handlers[scheme]; ok {
		return handler.StageIn(ctx, location, destPath, opts)
	}
	if s.fallback != nil {
		return s.fallback.StageIn(ctx, location, destPath, opts)
	}
	return fmt.Errorf("no stager registered for scheme %q", scheme)
}

// StageOut routes to a scheme-specific handler if the destination metadata
// indicates a known scheme (e.g., ws://, s3://), otherwise uses the fallback.
func (s *CompositeStager) StageOut(ctx context.Context, srcPath string, taskID string, opts StageOptions) (string, error) {
	// Check if metadata destination implies a specific scheme handler.
	if dest := opts.Metadata["destination"]; dest != "" {
		for scheme, handler := range s.handlers {
			if scheme != "" && strings.HasPrefix(dest, scheme+"://") {
				return handler.StageOut(ctx, srcPath, taskID, opts)
			}
		}
	}
	if s.fallback != nil {
		return s.fallback.StageOut(ctx, srcPath, taskID, opts)
	}
	return "", fmt.Errorf("no fallback stager configured for stage-out")
}

// Supports returns true if any registered handler supports the given scheme.
func (s *CompositeStager) Supports(scheme string) bool {
	if _, ok := s.handlers[scheme]; ok {
		return true
	}
	if s.fallback != nil {
		return s.fallback.Supports(scheme)
	}
	return false
}

// RegisterHandler adds a handler for a specific scheme.
func (s *CompositeStager) RegisterHandler(scheme string, handler Stager) {
	if s.handlers == nil {
		s.handlers = make(map[string]Stager)
	}
	s.handlers[scheme] = handler
}

// Verify interface compliance.
var _ Stager = (*CompositeStager)(nil)
