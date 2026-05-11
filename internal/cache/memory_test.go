package cache

import (
	"testing"

	"github.com/1URose/remote-config-system/internal/domain"
)

func TestUpdateKeys(t *testing.T) {
	store := New()
	store.ReplaceNamespace("payments", []domain.ConfigItem{
		{Namespace: "payments", Key: "flag_a", Value: "true", Version: 1},
		{Namespace: "payments", Key: "flag_b", Value: "1", Version: 1},
	})

	store.UpdateKeys("payments", []domain.ConfigItem{
		{Namespace: "payments", Key: "flag_b", Value: "2", Version: 2},
	})

	itemA, ok := store.Get("payments", "flag_a")
	if !ok {
		t.Fatalf("expected flag_a")
	}
	if itemA.Value != "true" {
		t.Fatalf("expected untouched key to stay true, got %s", itemA.Value)
	}
	itemB, ok := store.Get("payments", "flag_b")
	if !ok {
		t.Fatalf("expected flag_b")
	}
	if itemB.Value != "2" || itemB.Version != 2 {
		t.Fatalf("unexpected point update result: %+v", itemB)
	}
}

func TestFeatureCacheAndDeletes(t *testing.T) {
	store := New()
	store.ReplaceFeatures("payments", []domain.FeatureToggle{
		{Namespace: "payments", Key: "new-ui", Enabled: false, Version: 1},
	})

	store.UpdateFeatures("payments", []domain.FeatureToggle{
		{Namespace: "payments", Key: "new-ui", Enabled: true, Version: 2},
		{Namespace: "payments", Key: "beta-flow", Enabled: true, Version: 1},
	})

	feature, ok := store.GetFeature("payments", "new-ui")
	if !ok {
		t.Fatalf("expected new-ui feature")
	}
	if !feature.Enabled || feature.Version != 2 {
		t.Fatalf("unexpected feature update result: %+v", feature)
	}

	store.DeleteFeatures("payments", []string{"beta-flow"})
	if _, ok := store.GetFeature("payments", "beta-flow"); ok {
		t.Fatalf("expected beta-flow feature to be removed")
	}

	store.DeleteKeys("payments", []string{"missing"})
}
