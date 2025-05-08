package safemap

import (
	"sync"
)

type ISafeMap[K comparable, V any] interface {
	Get(key K) (value V, ok bool)
	Set(key K, value V)
	Delete(key K)
	LoadAndDelete(key K) (value V, loaded bool)
	Range(f func(key K, value V) bool)
	Refresh(key K) (value V, ok bool)
	// Exist get data if exists in cache, otherwise return false
	Exist(key K) (value V, ok bool)
}

type SafeMap[K comparable, V any] struct {
	m           sync.Map
	loader      func(key K) (V, error)
	fallback    V
	hasFallback bool
}

type Option[K comparable, V any] func(*SafeMap[K, V])

func NewSafeMapWithLoader[K comparable, V any](opts ...Option[K, V]) ISafeMap[K, V] {
	sm := &SafeMap[K, V]{}
	for _, opt := range opts {
		opt(sm)
	}
	return sm
}

func (sm *SafeMap[K, V]) Get(key K) (value V, ok bool) {
	if val, ok := sm.m.Load(key); ok {
		return val.(V), true
	}
	if sm.loader != nil {
		if loadedVal, err := sm.loader(key); err == nil {
			sm.Set(key, loadedVal)
			return loadedVal, true
		} else if sm.hasFallback {
			return sm.fallback, true
		}
	}
	return value, false
}

func (sm *SafeMap[K, V]) Exist(key K) (value V, ok bool) {
	if val, ok := sm.m.Load(key); ok {
		return val.(V), true
	}
	return value, false
}

func (sm *SafeMap[K, V]) Set(key K, value V) {
	sm.m.Store(key, value)
}

func (sm *SafeMap[K, V]) Delete(key K) {
	sm.m.Delete(key)
}

func (sm *SafeMap[K, V]) LoadAndDelete(key K) (value V, loaded bool) {
	val, loaded := sm.m.LoadAndDelete(key)
	return convert[V](val, loaded)
}

func (sm *SafeMap[K, V]) Range(f func(key K, value V) bool) {
	sm.m.Range(func(key, value any) bool {
		return f(key.(K), value.(V))
	})
}

func (sm *SafeMap[K, V]) Refresh(key K) (value V, ok bool) {
	if sm.loader != nil {
		if loadedVal, err := sm.loader(key); err == nil {
			sm.Set(key, loadedVal)
			return loadedVal, true
		} else if sm.hasFallback {
			sm.Delete(key) // Delete the old value if it exists
			return sm.fallback, true
		}
	}
	sm.Delete(key) // If no loader or loading failed without fallback, just delete
	return value, false
}

func WithLoader[K comparable, V any](loader func(key K) (V, error)) Option[K, V] {
	return func(sm *SafeMap[K, V]) {
		sm.loader = loader
	}
}

func WithFallbackValue[K comparable, V any](fallback V) Option[K, V] {
	return func(sm *SafeMap[K, V]) {
		sm.fallback = fallback
		sm.hasFallback = true
	}
}

// Helper function
func convert[T any](val any, ok bool) (T, bool) {
	if !ok {
		var temp T
		return temp, ok
	}
	return val.(T), ok
}
