package watcher

import (
	"testing"
	"time"

	"github.com/ya.ermakov/remote-config-system/remote-config-sdk/pkg/model"
)

func TestWatchCallbacks(t *testing.T) {
	registry := NewRegistry()
	keyCh := make(chan model.ConfigItem, 1)
	nsCh := make(chan []model.ConfigItem, 1)

	registry.AddKeyWatch("payments", "flag", func(item model.ConfigItem) {
		keyCh <- item
	})
	registry.AddNamespaceWatch("payments", func(items []model.ConfigItem) {
		nsCh <- items
	})

	registry.NotifyKeys("payments", []model.ConfigItem{
		{Namespace: "payments", Key: "flag", Value: "true", Version: 2},
	})

	select {
	case item := <-keyCh:
		if item.Key != "flag" {
			t.Fatalf("unexpected key callback payload %+v", item)
		}
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for key callback")
	}

	select {
	case items := <-nsCh:
		if len(items) != 1 || items[0].Key != "flag" {
			t.Fatalf("unexpected namespace callback payload %+v", items)
		}
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for namespace callback")
	}
}

