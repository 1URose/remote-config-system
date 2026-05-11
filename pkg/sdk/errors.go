package sdk

import "errors"

var (
	ErrRedisAddrRequired    = errors.New("redis address is required")
	ErrNamespaceRequired    = errors.New("namespace is required")
	ErrClientAlreadyStarted = errors.New("sdk client already started")
	ErrClientClosed         = errors.New("sdk client is closed")
)
