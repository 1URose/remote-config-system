package redis

import (
	_ "embed"

	goredis "github.com/redis/go-redis/v9"
)

//go:embed scripts/feature_upsert.lua
var upsertFeatureScriptSource string

var upsertFeatureScript = goredis.NewScript(upsertFeatureScriptSource)

//go:embed scripts/feature_delete.lua
var deleteFeatureScriptSource string

var deleteFeatureScript = goredis.NewScript(deleteFeatureScriptSource)
