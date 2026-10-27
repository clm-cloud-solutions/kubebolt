package copilot

import (
	"reflect"
	"testing"
)

// Every Usage field must go through SubtractUsage: a field it leaves out
// keeps the escalation model's tokens in the primary model's share, and the
// escalated turn is priced wrong without anything failing to compile (the
// 1h cache-write fields came from develop after SubtractUsage was written).
func TestSubtractUsage_CoversEveryField(t *testing.T) {
	var a, b Usage
	av := reflect.ValueOf(&a).Elem()
	bv := reflect.ValueOf(&b).Elem()
	for i := 0; i < av.NumField(); i++ {
		av.Field(i).SetInt(10)
		bv.Field(i).SetInt(4)
	}
	got := reflect.ValueOf(SubtractUsage(a, b))
	for i := 0; i < got.NumField(); i++ {
		if v := got.Field(i).Int(); v != 6 {
			t.Errorf("SubtractUsage leaves %s at %d, want 6", got.Type().Field(i).Name, v)
		}
	}
}

func TestSubtractUsage_FloorsAtZero(t *testing.T) {
	got := SubtractUsage(Usage{CacheCreation1hTokens: 3}, Usage{CacheCreation1hTokens: 5})
	if got.CacheCreation1hTokens != 0 {
		t.Errorf("CacheCreation1hTokens = %d, want 0", got.CacheCreation1hTokens)
	}
}
