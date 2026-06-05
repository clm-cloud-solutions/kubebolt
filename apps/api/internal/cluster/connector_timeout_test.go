package cluster

import (
	"testing"
	"time"
)

func TestEffectiveCacheSyncTimeout(t *testing.T) {
	cases := []struct {
		name string
		set  time.Duration
		want time.Duration
	}{
		{"unset → default", 0, defaultCacheSyncTimeout},
		{"below floor → default", 2 * time.Second, defaultCacheSyncTimeout},
		{"configured value wins", 60 * time.Second, 60 * time.Second},
		{"exactly floor ok", 5 * time.Second, 5 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Connector{}
			c.SetCacheSyncTimeout(tc.set)
			if got := c.effectiveCacheSyncTimeout(); got != tc.want {
				t.Fatalf("effectiveCacheSyncTimeout() with %s = %s, want %s", tc.set, got, tc.want)
			}
		})
	}
}
