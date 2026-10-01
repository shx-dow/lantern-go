package p2p

import "runtime"

// caseInsensitiveFS reports whether the host filesystem treats path casing as
// insignificant. Windows always does; macOS usually does but can be
// configured case-sensitively, where over-matching would be a false accept
// rather than a leak, so matching the common configuration is the safe choice.
var caseInsensitiveFS = runtime.GOOS == "windows" || runtime.GOOS == "darwin"
