package model

// New configuration and measurements belong to channel-model records only.
func (channel *Channel) validateMetadata(_ bool) error {
	return channel.ValidateModelConfigs()
}
