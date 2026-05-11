package sdk

func (c *Client) GetFeature(key string) (Feature, bool) {
	item, ok := c.cache.GetFeature(c.namespace, key)
	if !ok {
		return Feature{}, false
	}
	return newFeature(item), true
}

func (c *Client) IsFeatureEnabled(key string) bool {
	item, ok := c.GetFeature(key)
	return ok && item.Enabled
}
