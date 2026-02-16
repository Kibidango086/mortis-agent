package agent

import (
	"fmt"
	"sync"
	"time"
)

// IDGenerator 生成递增的 ID，1:1 还原 opencode 的 Identifier.ascending
type IDGenerator struct {
	mu       sync.Mutex
	counters map[string]int
}

// NewIDGenerator 创建新的 ID 生成器
func NewIDGenerator() *IDGenerator {
	return &IDGenerator{
		counters: make(map[string]int),
	}
}

// Ascending 生成递增的 ID，格式为 "prefix-N"
// 1:1 from opencode's Identifier.ascending
func (g *IDGenerator) Ascending(prefix string) string {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.counters[prefix]++
	return fmt.Sprintf("%s-%d", prefix, g.counters[prefix])
}

// Reset 重置特定前缀的计数器
func (g *IDGenerator) Reset(prefix string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.counters, prefix)
}

// ResetAll 重置所有计数器
func (g *IDGenerator) ResetAll() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.counters = make(map[string]int)
}

// globalIDGenerator 全局 ID 生成器实例
var globalIDGenerator = NewIDGenerator()

// GenerateID 使用全局生成器生成 ID
func GenerateID(prefix string) string {
	return globalIDGenerator.Ascending(prefix)
}

// ResetIDGenerator 重置全局生成器
func ResetIDGenerator() {
	globalIDGenerator.ResetAll()
}

// StreamIDGenerator 是每个流会话的 ID 生成器
type StreamIDGenerator struct {
	mu       sync.Mutex
	sequence int
	prefix   string
}

// NewStreamIDGenerator 创建新的流式 ID 生成器
func NewStreamIDGenerator(prefix string) *StreamIDGenerator {
	return &StreamIDGenerator{
		prefix: prefix,
	}
}

// Next 生成下一个 ID
func (g *StreamIDGenerator) Next() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sequence++
	return fmt.Sprintf("%s-%d", g.prefix, g.sequence)
}

// Now returns current timestamp in milliseconds (like Date.now() in JS)
func Now() int64 {
	return time.Now().UnixMilli()
}
