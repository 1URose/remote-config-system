package sdk

import "sync"

type watchRegistry struct {
	mu                sync.RWMutex
	keyWatchers       map[string][]func(Value)
	namespaceWatchers map[string][]func([]Value)
}

func newWatchRegistry() *watchRegistry {
	return &watchRegistry{
		keyWatchers:       make(map[string][]func(Value)),
		namespaceWatchers: make(map[string][]func([]Value)),
	}
}

func (r *watchRegistry) addKeyWatch(namespace, key string, cb func(Value)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keyWatchers[namespaceKey(namespace, key)] = append(r.keyWatchers[namespaceKey(namespace, key)], cb)
}

func (r *watchRegistry) addNamespaceWatch(namespace string, cb func([]Value)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.namespaceWatchers[namespace] = append(r.namespaceWatchers[namespace], cb)
}

func (r *watchRegistry) notify(namespace string, items []Value) {
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
		payload := append([]Value(nil), items...)
		go callback(payload)
	}
}

func namespaceKey(namespace, key string) string {
	return namespace + "::" + key
}
