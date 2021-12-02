package util

// Clamp return the clamped value of val.
func Clamp(val, min, max int64) int64 {
	if val < min {
		return min
	}
	if val > max {
		return max
	}

	return val
}
