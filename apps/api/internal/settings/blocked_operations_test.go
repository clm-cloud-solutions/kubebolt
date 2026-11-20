package settings

import (
	"errors"
	"reflect"
	"testing"
)

// failingStore answers every read with an error that is not "not found": the
// database is down.
type failingStore struct{}

func (failingStore) GetSetting(string) ([]byte, error) { return nil, errors.New("connection refused") }
func (failingStore) SetSetting(string, []byte) error   { return errors.New("connection refused") }

func TestBlockedOperations_NothingSetBlocksNothing(t *testing.T) {
	rt := newTestRuntime(t)
	got, err := rt.BlockedOperations()
	if err != nil || len(got.Connectors) != 0 {
		t.Fatalf("BlockedOperations = %+v, %v; want empty, nil", got, err)
	}
	if blocked, err := rt.OperationBlocked("aws", "s3.getobject"); blocked || err != nil {
		t.Fatalf("OperationBlocked = %v, %v; want false, nil", blocked, err)
	}
}

// An emergency switch must not open because the store hiccupped.
func TestBlockedOperations_AnUnreadableListFailsClosed(t *testing.T) {
	rt := &Runtime{store: failingStore{}}
	if _, err := rt.OperationBlocked("aws", "s3.getobject"); err == nil {
		t.Fatal("OperationBlocked on an unreadable store returned no error; the call would go through")
	}
	if _, err := rt.PutBlockedOperations(BlockedOperations{Connectors: map[string][]string{"aws": {"s3.*"}}}); err == nil {
		t.Fatal("PutBlockedOperations on an unreadable store returned no error")
	}
}

func TestBlockedOperations_ExactPrefixAndEverything(t *testing.T) {
	rt := newTestRuntime(t)
	if _, err := rt.PutBlockedOperations(BlockedOperations{Connectors: map[string][]string{
		"aws":   {" S3.GetObject ", "kms.*", "s3.getobject"},
		"azure": {"*"},
	}}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		conn, op string
		want     bool
	}{
		{"aws", "s3.GetObject", true},
		{"aws", "s3.listbuckets", false},
		{"aws", "kms.describekey", true},
		{"aws", "kmsx.describe", false}, // kms.* keeps its dot: another service
		{"azure", "storage_account_get", true},
		{"gcp", "compute.instances.list", false},
	}
	for _, c := range cases {
		got, err := rt.OperationBlocked(c.conn, c.op)
		if err != nil || got != c.want {
			t.Errorf("OperationBlocked(%s, %s) = %v, %v; want %v", c.conn, c.op, got, err, c.want)
		}
	}
	stored, _ := rt.BlockedOperations()
	if want := []string{"kms.*", "s3.getobject"}; !reflect.DeepEqual(stored.Connectors["aws"], want) {
		t.Errorf("aws list = %v, want lowercased, unique and sorted %v", stored.Connectors["aws"], want)
	}
}

func TestBlockedOperations_PutReplacesOnlyTheConnectorsItNames(t *testing.T) {
	rt := newTestRuntime(t)
	if _, err := rt.PutBlockedOperations(BlockedOperations{Connectors: map[string][]string{"aws": {"s3.*"}, "azure": {"*"}}}); err != nil {
		t.Fatal(err)
	}
	got, err := rt.PutBlockedOperations(BlockedOperations{Connectors: map[string][]string{"azure": {}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string][]string{"aws": {"s3.*"}}; !reflect.DeepEqual(got.Connectors, want) {
		t.Fatalf("after clearing azure = %v, want %v", got.Connectors, want)
	}
}

func TestBlockedOperations_RefusesWhatIsNotAnOperation(t *testing.T) {
	rt := newTestRuntime(t)
	for name, in := range map[string]map[string][]string{
		"unknown connector":   {"oracle": {"x"}},
		"space inside":        {"aws": {"s3 getobject"}},
		"star in the middle":  {"aws": {"s3.*object"}},
		"leading punctuation": {"aws": {".s3"}},
	} {
		_, err := rt.PutBlockedOperations(BlockedOperations{Connectors: in})
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want a ValidationError", name, err)
		}
	}
}
