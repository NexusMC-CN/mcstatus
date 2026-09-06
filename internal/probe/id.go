package probe

import (
	"crypto/rand"
	"encoding/hex"
)

func newJobID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "probe-job-fallback"
	}
	return hex.EncodeToString(buf)
}
