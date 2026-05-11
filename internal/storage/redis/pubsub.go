package redis

import (
	"context"
	"encoding/json"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/1URose/remote-config-system/internal/domain"
)

type PubSub struct {
	client *goredis.Client
}

func NewPubSub(client *goredis.Client) *PubSub {
	return &PubSub{client: client}
}

func (p *PubSub) Subscribe(ctx context.Context, namespace string) *goredis.PubSub {
	return p.client.Subscribe(ctx, updatesChannel(namespace))
}

func (p *PubSub) ParseEvent(payload string) (domain.ConfigUpdateEvent, error) {
	var event domain.ConfigUpdateEvent
	err := json.Unmarshal([]byte(payload), &event)
	return event, err
}

func (p *PubSub) PublishFlush(ctx context.Context, namespace, updatedBy, requestID string) error {
	payload, err := json.Marshal(domain.ConfigUpdateEvent{
		Namespace: namespace,
		Operation: "flush",
		UpdatedAt: time.Now().UTC(),
		UpdatedBy: updatedBy,
		RequestID: requestID,
	})
	if err != nil {
		return err
	}
	return p.client.Publish(ctx, updatesChannel(namespace), payload).Err()
}
