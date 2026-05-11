package sdk

import (
	"strconv"
	"time"

	"github.com/1URose/remote-config-system/internal/domain"
)

type Value struct {
	Namespace string
	Key       string
	Raw       string
	Type      string
	Version   int64
	IsSecret  bool
	UpdatedAt time.Time
	UpdatedBy string
}

func (v Value) Bool() (bool, error) {
	return strconv.ParseBool(v.Raw)
}

func (v Value) Int() (int, error) {
	return strconv.Atoi(v.Raw)
}

func newValue(item domain.ConfigItem) Value {
	return Value{
		Namespace: item.Namespace,
		Key:       item.Key,
		Raw:       item.Value,
		Type:      item.Type,
		Version:   item.Version,
		IsSecret:  item.IsSecret,
		UpdatedAt: item.UpdatedAt,
		UpdatedBy: item.UpdatedBy,
	}
}
