package audit

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func setEnvParams(value string) map[string]any {
	var params map[string]any
	_ = json.Unmarshal([]byte(`{"fromEnv":[{"container":"app","env":[`+
		`{"name":"DB_PASSWORD","kind":"literal","value":"`+value+`"},`+
		`{"name":"TOKEN","kind":"secret","valueFrom":{"secretKeyRef":{"name":"s","key":"k"}}}]}],`+
		`"toEnv":[{"container":"app","env":[{"name":"LOG","kind":"literal","value":"debug"}]}],"triggerRollout":true}`), &params)
	return params
}

func TestDropEnvValues(t *testing.T) {
	p := setEnvParams("drop-s3cret")
	if !DropEnvValues(p) {
		t.Fatal("nothing dropped")
	}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "drop-s3cret") || strings.Contains(string(raw), `"value"`) {
		t.Errorf("a value survived: %s", raw)
	}
	for _, kept := range []string{"DB_PASSWORD", "secretKeyRef", `"triggerRollout":true`} {
		if !strings.Contains(string(raw), kept) {
			t.Errorf("lost %s: %s", kept, raw)
		}
	}
	if DropEnvValues(p) {
		t.Error("a second pass reported a change")
	}
	if DropEnvValues(map[string]any{"replicas": 3.0, "env": "not a list"}) {
		t.Error("params without an env list reported a change")
	}
}

// Records written before params were scrubbed at write time are rewritten once.
func TestBoltStore_DropEnvValues(t *testing.T) {
	db, err := bolt.Open(filepath.Join(t.TempDir(), "audit.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bucket := []byte("kobi_actions")
	if err := db.Update(func(tx *bolt.Tx) error { _, err := tx.CreateBucket(bucket); return err }); err != nil {
		t.Fatal(err)
	}
	s := NewBoltStore(db, bucket)
	now := time.Now().UTC()
	_ = s.Append(&Record{ID: "1", Timestamp: now, Action: "set_env", Result: "success", Params: setEnvParams("bolt-s3cret")})
	_ = s.Append(&Record{ID: "2", Timestamp: now.Add(time.Second), Action: "scale_workload", Result: "success", Params: map[string]any{"replicas": 3.0}})

	n, err := s.DropEnvValues()
	if err != nil || n != 1 {
		t.Fatalf("rewrote %d records (err %v), want 1", n, err)
	}
	recs, _ := s.ListOrg("", 0)
	raw, _ := json.Marshal(recs)
	if strings.Contains(string(raw), "bolt-s3cret") || len(recs) != 2 {
		t.Errorf("after the pass: %s", raw)
	}
	if n, _ := s.DropEnvValues(); n != 0 {
		t.Errorf("a second pass rewrote %d records", n)
	}
}
