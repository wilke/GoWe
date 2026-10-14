package server

import (
	"context"

	"github.com/me/gowe/internal/tokencheck"
	"github.com/me/gowe/pkg/model"
)

// Error codes of the submit-time token-policy check (see internal/tokencheck).
const (
	ErrToolNotBlessed           = tokencheck.ErrToolNotBlessed
	ErrTokenToolOverrideRefused = tokencheck.ErrTokenToolOverrideRefused
)

// Token-blessing modes (mirrors scheduler.TokenBlessing*).
const (
	tokenBlessingEnforce = "enforce"
	tokenBlessingDev     = "dev"
)

// WithTokenBlessingMode sets the --token-blessing mode ("enforce", the
// default, or "dev"). Dev mode only downgrades an unblessed token request from
// a 400 to a response warning; blessed tools behave identically in both.
func WithTokenBlessingMode(mode string) Option {
	return func(s *Server) {
		if mode == tokenBlessingDev {
			s.tokenBlessing = tokenBlessingDev
		} else {
			s.tokenBlessing = tokenBlessingEnforce
		}
	}
}

func (s *Server) tokenBlessingMode() string {
	if s.tokenBlessing == tokenBlessingDev {
		return tokenBlessingDev
	}
	return tokenBlessingEnforce
}

func (s *Server) tokenChecker() *tokencheck.Checker {
	return &tokencheck.Checker{Parser: s.parser, Store: s.store, Logger: s.logger, Dev: s.tokenBlessingMode() == tokenBlessingDev}
}

// checkTokenRequests is the submit-time provider-token fast-fail.
func (s *Server) checkTokenRequests(ctx context.Context, wf *model.Workflow, inputs map[string]any, labels map[string]string, hasUserToken bool) (*model.APIError, []model.FieldError) {
	return s.tokenChecker().Check(ctx, wf, inputs, labels, hasUserToken)
}

func (s *Server) unblessedTokenToolWarnings(ctx context.Context, rawCWL string) []string {
	return s.tokenChecker().UnblessedToolWarnings(ctx, rawCWL)
}
