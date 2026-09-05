package format

import "fmt"

// Bytes renders a byte count in human units.
func Bytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// Ratio returns the fraction of total completed, in [0,1]. Unknown or
// non-positive totals report 0 so progress bars never divide by zero.
func Ratio(b, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(b) / float64(total)
}
