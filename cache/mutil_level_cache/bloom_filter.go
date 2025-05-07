package mutil_level_cache

import "hash/fnv"

// 布隆过滤器
type BloomFilter struct {
	bits     []bool
	size     int
	hashFunc []func(string) uint32
}

func NewBloomFilter(size int, hashCount int) *BloomFilter {
	bf := &BloomFilter{
		bits:     make([]bool, size),
		size:     size,
		hashFunc: make([]func(string) uint32, hashCount),
	}

	// 初始化哈希函数
	for i := 0; i < hashCount; i++ {
		seed := uint32(i)
		bf.hashFunc[i] = func(s string) uint32 {
			h := fnv.New32a()
			h.Write([]byte(s))
			return h.Sum32() + seed
		}
	}

	return bf
}

func (bf *BloomFilter) Add(key string) {
	for _, hash := range bf.hashFunc {
		bf.bits[hash(key)%uint32(bf.size)] = true
	}
}

func (bf *BloomFilter) Contains(key string) bool {
	for _, hash := range bf.hashFunc {
		if !bf.bits[hash(key)%uint32(bf.size)] {
			return false
		}
	}
	return true
}
