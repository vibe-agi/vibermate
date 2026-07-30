package runtimepersistence

import "time"

func toUnixMillis(value time.Time) int64 {
	return value.UTC().UnixMilli()
}

func fromUnixMillis(value int64) time.Time {
	return time.UnixMilli(value).UTC()
}
