package sdk

func (ns *Namespace) GetFeature(key string) (Feature, bool) {
	item, ok := ns.client.cache.GetFeature(ns.namespace, key)
	if !ok {
		return Feature{}, false
	}
	return newFeature(item), true
}

func (ns *Namespace) IsFeatureEnabled(key string) bool {
	item, ok := ns.GetFeature(key)
	return ok && item.Enabled
}
