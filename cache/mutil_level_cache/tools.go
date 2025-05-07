package mutil_level_cache

// EstimateSize  预估对象大小
func EstimateSize(v interface{}) int {
	// 实际应用中，应该根据对象类型更精确地计算大小
	// 这里是简化的实现
	switch val := v.(type) {
	case string:
		return len(val)
	case []byte:
		return len(val)
	default:
		// 默认大小，实际应根据对象类型详细计算
		return 64
	}
}
