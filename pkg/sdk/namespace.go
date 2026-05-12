package sdk

import (
	"context"
	"strconv"
)

type Namespace struct {
	client    *Client
	namespace string
}

func (ns *Namespace) Get(key string) (Value, bool) {
	item, ok := ns.client.cache.Get(ns.namespace, key)
	if !ok {
		return Value{}, false
	}
	return newValue(item), true
}

func (ns *Namespace) GetRaw(key string) (Value, bool) {
	return ns.Get(key)
}

func (ns *Namespace) GetString(key string) (string, bool) {
	value, ok := ns.Get(key)
	if !ok {
		return "", false
	}
	return value.Raw, true
}

func (ns *Namespace) GetBool(key string) (bool, bool) {
	value, ok := ns.GetString(key)
	if !ok {
		return false, false
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, false
	}
	return parsed, true
}

func (ns *Namespace) GetInt(key string) (int, bool) {
	value, ok := ns.GetString(key)
	if !ok {
		return 0, false
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func (ns *Namespace) Watch(key string, cb func(Value)) {
	ns.client.watchers.addKeyWatch(ns.namespace, key, cb)
}

func (ns *Namespace) WatchNamespace(cb func([]Value)) {
	ns.client.watchers.addNamespaceWatch(ns.namespace, cb)
}

func (ns *Namespace) Reload() error {
	return ns.client.reloadNamespaceWithContext(context.Background(), ns.namespace)
}

func (ns *Namespace) ReloadKeys(keys []string) error {
	return ns.client.reloadKeysWithContext(context.Background(), ns.namespace, keys)
}

func (ns *Namespace) Flush() error {
	return ns.client.flushNamespace(ns.namespace)
}
