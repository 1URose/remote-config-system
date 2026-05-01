package remoteconfig

import (
	"context"

	sdk "github.com/ya.ermakov/remote-config-system/remote-config-sdk/pkg/remoteconfig"
)

type Options = sdk.Options
type Client = sdk.Client
type Stats = sdk.Stats

func New(ctx context.Context, opts Options) (*Client, error) {
	return sdk.New(ctx, opts)
}
