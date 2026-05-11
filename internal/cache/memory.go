package cache

import (
	"sync"

	"github.com/1URose/remote-config-system/internal/domain"
)

type Memory struct {
	mu       sync.RWMutex
	configs  map[string]map[string]domain.ConfigItem
	features map[string]map[string]domain.FeatureToggle
}

func New() *Memory {
	return &Memory{
		configs:  make(map[string]map[string]domain.ConfigItem),
		features: make(map[string]map[string]domain.FeatureToggle),
	}
}

func (m *Memory) Get(namespace, key string) (domain.ConfigItem, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	namespaceItems, ok := m.configs[namespace]
	if !ok {
		return domain.ConfigItem{}, false
	}
	item, ok := namespaceItems[key]
	return item, ok
}

func (m *Memory) GetFeature(namespace, key string) (domain.FeatureToggle, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	namespaceItems, ok := m.features[namespace]
	if !ok {
		return domain.FeatureToggle{}, false
	}
	item, ok := namespaceItems[key]
	return item, ok
}

func (m *Memory) ReplaceNamespace(namespace string, items []domain.ConfigItem) {
	namespaceItems := make(map[string]domain.ConfigItem, len(items))
	for _, item := range items {
		namespaceItems[item.Key] = item
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.configs[namespace] = namespaceItems
}

func (m *Memory) ReplaceFeatures(namespace string, items []domain.FeatureToggle) {
	namespaceItems := make(map[string]domain.FeatureToggle, len(items))
	for _, item := range items {
		namespaceItems[item.Key] = item
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.features[namespace] = namespaceItems
}

func (m *Memory) UpdateKeys(namespace string, items []domain.ConfigItem) []domain.ConfigItem {
	m.mu.Lock()
	defer m.mu.Unlock()

	current := m.configs[namespace]
	if current == nil {
		current = make(map[string]domain.ConfigItem)
	}
	changed := make([]domain.ConfigItem, 0, len(items))
	for _, item := range items {
		current[item.Key] = item
		changed = append(changed, item)
	}
	m.configs[namespace] = current
	return changed
}

func (m *Memory) UpdateFeatures(namespace string, items []domain.FeatureToggle) []domain.FeatureToggle {
	m.mu.Lock()
	defer m.mu.Unlock()

	current := m.features[namespace]
	if current == nil {
		current = make(map[string]domain.FeatureToggle)
	}
	changed := make([]domain.FeatureToggle, 0, len(items))
	for _, item := range items {
		current[item.Key] = item
		changed = append(changed, item)
	}
	m.features[namespace] = current
	return changed
}

func (m *Memory) DeleteNamespace(namespace string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.configs, namespace)
	delete(m.features, namespace)
}

func (m *Memory) DeleteKeys(namespace string, keys []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	current := m.configs[namespace]
	for _, key := range keys {
		delete(current, key)
	}
	if len(current) == 0 {
		delete(m.configs, namespace)
	}
}

func (m *Memory) DeleteFeatures(namespace string, keys []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	current := m.features[namespace]
	for _, key := range keys {
		delete(current, key)
	}
	if len(current) == 0 {
		delete(m.features, namespace)
	}
}

func (m *Memory) Snapshot(namespace string) []domain.ConfigItem {
	m.mu.RLock()
	defer m.mu.RUnlock()

	namespaceItems := m.configs[namespace]
	if namespaceItems == nil {
		return nil
	}
	items := make([]domain.ConfigItem, 0, len(namespaceItems))
	for _, item := range namespaceItems {
		items = append(items, item)
	}
	return items
}

func (m *Memory) SnapshotFeatures(namespace string) []domain.FeatureToggle {
	m.mu.RLock()
	defer m.mu.RUnlock()

	namespaceItems := m.features[namespace]
	if namespaceItems == nil {
		return nil
	}
	items := make([]domain.FeatureToggle, 0, len(namespaceItems))
	for _, item := range namespaceItems {
		items = append(items, item)
	}
	return items
}

func (m *Memory) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	total := 0
	for _, namespaceItems := range m.configs {
		total += len(namespaceItems)
	}
	for _, namespaceItems := range m.features {
		total += len(namespaceItems)
	}
	return total
}
