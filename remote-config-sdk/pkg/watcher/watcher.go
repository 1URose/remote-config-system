package watcher

import (
	"sync"

	"github.com/ya.ermakov/remote-config-system/remote-config-sdk/pkg/model"
)

type KeyCallback func(model.ConfigItem)
type NamespaceCallback func([]model.ConfigItem)

type Registry struct {
	mu               sync.RWMutex
	keyWatchers      map[string][]KeyCallback
	namespaceWatchers map[string][]NamespaceCallback
}

func NewRegistry() *Registry {
	return &Registry{
		keyWatchers:      make(map[string][]KeyCallback),
		namespaceWatchers: make(map[string][]NamespaceCallback),
	}
}

func (r *Registry) AddKeyWatch(namespace, key string, cb KeyCallback) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keyWatchers[namespaceKey(namespace, key)] = append(r.keyWatchers[namespaceKey(namespace, key)], cb)
}

func (r *Registry) AddNamespaceWatch(namespace string, cb NamespaceCallback) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.namespaceWatchers[namespace] = append(r.namespaceWatchers[namespace], cb)
}

func (r *Registry) NotifyKeys(namespace string, items []model.ConfigItem) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, item := range items {
		for _, cb := range r.keyWatchers[namespaceKey(namespace, item.Key)] {
			callback := cb
			value := item
			go callback(value)
		}
	}
	for _, cb := range r.namespaceWatchers[namespace] {
		callback := cb
		payload := append([]model.ConfigItem(nil), items...)
		go callback(payload)
	}
}

func namespaceKey(namespace, key string) string {
	return namespace + "::" + key
}

