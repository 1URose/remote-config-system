package cache

import (
	"fmt"
	"sync"

	"github.com/ya.ermakov/remote-config-system/remote-config-sdk/pkg/model"
)

type Store struct {
	mu   sync.RWMutex
	data map[string]map[string]model.ConfigItem
}

func NewStore() *Store {
	return &Store{
		data: make(map[string]map[string]model.ConfigItem),
	}
}

func (s *Store) Get(namespace, key string) (model.ConfigItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	namespaceItems, ok := s.data[namespace]
	if !ok {
		return model.ConfigItem{}, fmt.Errorf("namespace %q not loaded", namespace)
	}
	item, ok := namespaceItems[key]
	if !ok {
		return model.ConfigItem{}, fmt.Errorf("key %q not found in namespace %q", key, namespace)
	}
	return item, nil
}

func (s *Store) ReplaceNamespace(namespace string, items []model.ConfigItem) {
	namespaceItems := make(map[string]model.ConfigItem, len(items))
	for _, item := range items {
		namespaceItems[item.Key] = item
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[namespace] = namespaceItems
}

func (s *Store) DeleteNamespace(namespace string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, namespace)
}

func (s *Store) UpdateKeys(namespace string, items []model.ConfigItem) []model.ConfigItem {
	s.mu.Lock()
	defer s.mu.Unlock()

	current := s.data[namespace]
	if current == nil {
		current = make(map[string]model.ConfigItem)
	}
	changed := make([]model.ConfigItem, 0, len(items))
	for _, item := range items {
		current[item.Key] = item
		changed = append(changed, item)
	}
	s.data[namespace] = current
	return changed
}

func (s *Store) Snapshot(namespace string) []model.ConfigItem {
	s.mu.RLock()
	defer s.mu.RUnlock()

	namespaceItems := s.data[namespace]
	if namespaceItems == nil {
		return nil
	}
	items := make([]model.ConfigItem, 0, len(namespaceItems))
	for _, item := range namespaceItems {
		items = append(items, item)
	}
	return items
}

func (s *Store) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := 0
	for _, namespaceItems := range s.data {
		total += len(namespaceItems)
	}
	return total
}
