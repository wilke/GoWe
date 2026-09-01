package server

import (
	"container/list"
	"context"
	"sync"
)

// defaultWorkflowNameCacheSize bounds the LRU cache workflowNameFor uses to
// resolve a submission's workflow name for Prometheus task-metric labeling.
const defaultWorkflowNameCacheSize = 1024

// workflowNameCache is a small LRU cache of submission ID → workflow name.
// It exists so the hot worker-report request path (handleWorkerTaskComplete)
// doesn't pay a store round trip on every report just to label a metric:
// store.GetSubmissionMeta's lean SELECT (workflow_name + labels only, no
// inputs/outputs/tasks) backs a cache miss.
type workflowNameCache struct {
	mu       sync.Mutex
	capacity int
	ll       *list.List
	items    map[string]*list.Element
}

type workflowNameCacheEntry struct {
	submissionID string
	workflowName string
}

func newWorkflowNameCache(capacity int) *workflowNameCache {
	if capacity <= 0 {
		capacity = defaultWorkflowNameCacheSize
	}
	return &workflowNameCache{
		capacity: capacity,
		ll:       list.New(),
		items:    make(map[string]*list.Element),
	}
}

func (c *workflowNameCache) get(submissionID string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[submissionID]
	if !ok {
		return "", false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*workflowNameCacheEntry).workflowName, true
}

func (c *workflowNameCache) put(submissionID, workflowName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[submissionID]; ok {
		el.Value.(*workflowNameCacheEntry).workflowName = workflowName
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&workflowNameCacheEntry{submissionID: submissionID, workflowName: workflowName})
	c.items[submissionID] = el
	if c.ll.Len() > c.capacity {
		oldest := c.ll.Back()
		if oldest != nil {
			c.ll.Remove(oldest)
			delete(c.items, oldest.Value.(*workflowNameCacheEntry).submissionID)
		}
	}
}

// workflowNameFor resolves a submission's workflow name for metric
// labeling: an LRU hit avoids the store entirely; a miss falls back to
// store.GetSubmissionMeta and populates the cache. Returns "" on any lookup
// failure (missing submission, store error) — a label lookup miss must
// never block a worker's completion report.
func (s *Server) workflowNameFor(ctx context.Context, submissionID string) string {
	if name, ok := s.workflowNames.get(submissionID); ok {
		return name
	}
	meta, err := s.store.GetSubmissionMeta(ctx, submissionID)
	if err != nil || meta == nil {
		return ""
	}
	s.workflowNames.put(submissionID, meta.WorkflowName)
	return meta.WorkflowName
}
