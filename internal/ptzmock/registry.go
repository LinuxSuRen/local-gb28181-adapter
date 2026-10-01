package ptzmock

import (
	"sync"
)

// Registry 管理每个摄像头（profile）对应的虚拟云台节点。
type Registry struct {
	mu    sync.Mutex
	nodes map[string]*Node
}

// NewRegistry 创建注册表。
func NewRegistry() *Registry {
	return &Registry{nodes: map[string]*Node{}}
}

// Get 返回指定摄像头的节点，不存在时惰性创建。
func (r *Registry) Get(camID string) *Node {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n, ok := r.nodes[camID]; ok {
		return n
	}
	n := NewNode()
	r.nodes[camID] = n
	return n
}

// Remove 删除节点（摄像头被删除时调用）。
func (r *Registry) Remove(camID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.nodes, camID)
}
