package mutil_level_cache

import "sync"

// 访问计数器
type AccessCounter[K comparable] struct {
	counts    map[K]int
	mu        sync.RWMutex
	decayRate float64 // 衰减率
}

func NewAccessCounter[K comparable](decayRate float64) *AccessCounter[K] {
	return &AccessCounter[K]{
		counts:    make(map[K]int),
		decayRate: decayRate,
	}
}

func (ac *AccessCounter[K]) Increment(key K) {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	ac.counts[key]++
}

func (ac *AccessCounter[K]) Get(key K) int {
	ac.mu.RLock()
	defer ac.mu.RUnlock()
	return ac.counts[key]
}

func (ac *AccessCounter[K]) Remove(key K) {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	delete(ac.counts, key)
}

func (ac *AccessCounter[K]) Decay() {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	for k, v := range ac.counts {
		ac.counts[k] = int(float64(v) * ac.decayRate)
	}
}
