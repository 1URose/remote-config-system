package sdk

import (
	"log/slog"
	"strings"
	"time"
)

type Option func(*options) error

type options struct {
	redisAddr     string
	redisPassword string
	redisDB       int
	namespace     string
	logger        *slog.Logger
	retryInterval time.Duration
}

func defaultOptions() options {
	return options{
		logger:        slog.Default(),
		retryInterval: 2 * time.Second,
	}
}

func WithRedisAddr(addr string) Option {
	return func(opts *options) error {
		opts.redisAddr = strings.TrimSpace(addr)
		return nil
	}
}

func WithRedisPassword(password string) Option {
	return func(opts *options) error {
		opts.redisPassword = password
		return nil
	}
}

func WithRedisDB(db int) Option {
	return func(opts *options) error {
		opts.redisDB = db
		return nil
	}
}

func WithNamespace(namespace string) Option {
	return func(opts *options) error {
		opts.namespace = strings.TrimSpace(namespace)
		return nil
	}
}

func WithLogger(logger *slog.Logger) Option {
	return func(opts *options) error {
		if logger != nil {
			opts.logger = logger
		}
		return nil
	}
}

func WithRetryInterval(interval time.Duration) Option {
	return func(opts *options) error {
		if interval > 0 {
			opts.retryInterval = interval
		}
		return nil
	}
}
