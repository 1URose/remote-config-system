package cache

import (
	"testing"

	"github.com/ya.ermakov/remote-config-system/remote-config-sdk/pkg/model"
)

func TestUpdateKeys(t *testing.T) {
	store := NewStore()
	store.ReplaceNamespace("payments", []model.ConfigItem{
		{Namespace: "payments", Key: "flag_a", Value: "true", Version: 1},
		{Namespace: "payments", Key: "flag_b", Value: "1", Version: 1},
	})

	store.UpdateKeys("payments", []model.ConfigItem{
		{Namespace: "payments", Key: "flag_b", Value: "2", Version: 2},
	})

	itemA, err := store.Get("payments", "flag_a")
	if err != nil {
		t.Fatalf("expected flag_a: %v", err)
	}
	if itemA.Value != "true" {
		t.Fatalf("expected untouched key to stay true, got %s", itemA.Value)
	}
	itemB, err := store.Get("payments", "flag_b")
	if err != nil {
		t.Fatalf("expected flag_b: %v", err)
	}
	if itemB.Value != "2" || itemB.Version != 2 {
		t.Fatalf("unexpected point update result: %+v", itemB)
	}
}

