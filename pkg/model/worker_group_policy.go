package model

import "time"

// WorkerGroupPolicy is one rule of the worker-group access policy (#261): the
// submitters who may send tasks to Group. Server admins may also use it when
// Admins is true. The default group is stored as "default". Rules live in the
// database and are managed by server admins through the admin API
// (internal/groupacl enforces them).
type WorkerGroupPolicy struct {
	Group     string    `json:"group"`
	Users     []string  `json:"users"`
	Admins    bool      `json:"admins"`
	UpdatedBy string    `json:"updated_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
