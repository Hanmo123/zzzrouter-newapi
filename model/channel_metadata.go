package model

import (
	"errors"
	"math"
)

// validateMetadata validates the current display/observation snapshot, not billing
// or routing state. A nil observation means unknown, never healthy by default.
func (channel *Channel) validateMetadata(create bool) error {
	if channel.PublicRatio == nil && create {
		ratio := 1.0
		channel.PublicRatio = &ratio
	}
	if channel.PublicRatio != nil {
		ratio := *channel.PublicRatio
		if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio <= 0 {
			return errors.New("public_ratio must be a finite positive number")
		}
	}
	if channel.TTFTMs != nil && *channel.TTFTMs < 0 {
		return errors.New("ttft_ms must be a non-negative integer")
	}
	if channel.LastCheckedAt != nil && (*channel.LastCheckedAt < 0 || *channel.LastCheckedAt > 253402300799) {
		return errors.New("last_checked_at must be Unix seconds between 0 and 253402300799")
	}
	return nil
}
