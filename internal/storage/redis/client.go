package redis

import (
	"fmt"

	goredis "github.com/redis/go-redis/v9"
)

type ClientConfig struct {
	Addr     string
	Password string
	DB       int
}

func NewClient(cfg ClientConfig) *goredis.Client {
	return goredis.NewClient(&goredis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})
}

func configRedisKey(namespace, key string) string {
	return fmt.Sprintf("config:%s:%s", namespace, key)
}

func namespaceSetKey(namespace string) string {
	return fmt.Sprintf("config_keys:%s", namespace)
}

func featureRedisKey(namespace, key string) string {
	return fmt.Sprintf("feature:%s:%s", namespace, key)
}

func featureSetKey(namespace string) string {
	return fmt.Sprintf("feature_keys:%s", namespace)
}

func auditListKey(namespace string) string {
	return fmt.Sprintf("audit:%s", namespace)
}

func updatesChannel(namespace string) string {
	return fmt.Sprintf("events:%s", namespace)
}
