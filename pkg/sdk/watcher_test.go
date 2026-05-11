package sdk

import (
	"testing"
	"time"
)

func TestWatchCallbacks(t *testing.T) {
	registry := newWatchRegistry()
	keyCh := make(chan Value, 1)
	nsCh := make(chan []Value, 1)

	registry.addKeyWatch("payments", "flag", func(item Value) {
		keyCh <- item
	})
	registry.addNamespaceWatch("payments", func(items []Value) {
		nsCh <- items
	})

	registry.notify("payments", []Value{
		{Namespace: "payments", Key: "flag", Raw: "true", Version: 2},
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
