package executor

import (
	"fmt"
	"log/slog"

	"github.com/me/gowe/pkg/model"
)

// Registry maps ExecutorType values to their Executor implementations.
// Registration happens at startup before concurrent access, so no mutex is needed.
type Registry struct {
	executors map[model.ExecutorType]Executor
	logger    *slog.Logger
}

// NewRegistry creates an empty Registry.
func NewRegistry(logger *slog.Logger) *Registry {
	return &Registry{
		executors: make(map[model.ExecutorType]Executor),
		logger:    logger.With("component", "executor-registry"),
	}
}

// Register adds an Executor to the registry, keyed by its Type().
func (r *Registry) Register(exec Executor) {
	t := exec.Type()
	r.executors[t] = exec
	r.logger.Info("executor registered", "type", t)
}

// Get returns the Executor for the given type or an error if none is registered.
func (r *Registry) Get(t model.ExecutorType) (Executor, error) {
	exec, ok := r.executors[t]
	if !ok {
		return nil, fmt.Errorf("no executor registered for type %q", t)
	}
	return exec, nil
}
