package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// Blocked operations (debt-09) — the platform's emergency list of cloud
// connector operations no call may run, edited without a release. Each
// connector applies it before its own allowlist: an entry stops an operation
// the reviewed allowlist would let through (one later found to leak data, or
// one an org keeps abusing), and `*` pauses a connector entirely.
//
// It fails closed. When the list cannot be read, OperationBlocked returns an
// error and the connector refuses the call: a switch meant for emergencies
// must not open because the store hiccupped. "Nothing set" is not a failure.
//
// Install-global, like the rate limits: a platform admin sets it and it
// applies to every org on the next call, on every replica.

const blockedOperationsKey = "blocked_operations"

// BlockableConnectors are the connectors the list may name. MIRROR of the
// catalog's connector ids (settings cannot import mcpcatalog);
// TestBlockableConnectorsMatchTheCatalog holds them together.
var BlockableConnectors = []string{"aws", "azure", "gcp"}

// BlockedOperations is what the platform stores and what GET and PUT carry:
// per connector, the blocked entries, lowercased, unique and sorted.
//
// An entry names an operation the way the connector does: AWS
// `service.operation` (s3.getobject), Azure the command name
// (storage_account_get), GCP the method. A trailing `*` blocks a prefix
// (s3.*), and `*` alone blocks every call of the connector.
type BlockedOperations struct {
	Connectors map[string][]string `json:"connectors"`
}

const (
	maxBlockedPerConnector = 200
	maxBlockedEntryLen     = 128
)

var blockedEntryRE = regexp.MustCompile(`^(\*|[a-z0-9][a-z0-9._:/-]*\*?)$`)

// normalizeBlocked trims, lowercases, drops empties, dedupes and sorts every
// list, and refuses an unknown connector or a malformed entry with the field
// the form shows it under.
func normalizeBlocked(in BlockedOperations) (BlockedOperations, error) {
	known := map[string]bool{}
	for _, c := range BlockableConnectors {
		known[c] = true
	}
	out := BlockedOperations{Connectors: map[string][]string{}}
	for conn, entries := range in.Connectors {
		if !known[conn] {
			return BlockedOperations{}, &ValidationError{Field: "connectors." + conn, Message: "unknown connector"}
		}
		seen := map[string]bool{}
		list := []string{}
		for _, e := range entries {
			e = strings.ToLower(strings.TrimSpace(e))
			if e == "" || seen[e] {
				continue
			}
			if len(e) > maxBlockedEntryLen || !blockedEntryRE.MatchString(e) {
				return BlockedOperations{}, &ValidationError{Field: "connectors." + conn,
					Message: fmt.Sprintf("%q is not an operation name: use letters, digits and . _ - : /, with an optional * at the end", e)}
			}
			seen[e] = true
			list = append(list, e)
		}
		if len(list) > maxBlockedPerConnector {
			return BlockedOperations{}, &ValidationError{Field: "connectors." + conn,
				Message: fmt.Sprintf("at most %d entries per connector", maxBlockedPerConnector)}
		}
		sort.Strings(list)
		if len(list) > 0 {
			out.Connectors[conn] = list
		}
	}
	return out, nil
}

// BlockedOperations reads the stored list. Nothing stored is an empty list;
// a store that cannot be read, or a record that does not decode, is an error.
func (r *Runtime) BlockedOperations() (BlockedOperations, error) {
	empty := BlockedOperations{Connectors: map[string][]string{}}
	if r == nil || r.store == nil {
		return empty, nil
	}
	raw, err := r.store.GetSetting(blockedOperationsKey)
	if errors.Is(err, auth.ErrSettingNotFound) {
		return empty, nil
	}
	if err != nil {
		return BlockedOperations{}, fmt.Errorf("read blocked operations: %w", err)
	}
	var stored BlockedOperations
	if err := json.Unmarshal(raw, &stored); err != nil {
		return BlockedOperations{}, fmt.Errorf("decode blocked operations: %w", err)
	}
	if stored.Connectors == nil {
		stored.Connectors = map[string][]string{}
	}
	return stored, nil
}

// OperationBlocked reports whether connector's operation op is on the list.
// An error means the list could not be read: the caller refuses the call.
func (r *Runtime) OperationBlocked(connector, op string) (bool, error) {
	list, err := r.BlockedOperations()
	if err != nil {
		return false, err
	}
	op = strings.ToLower(strings.TrimSpace(op))
	for _, e := range list.Connectors[connector] {
		if blockedMatch(e, op) {
			return true, nil
		}
	}
	return false, nil
}

// blockedMatch: an exact name, a prefix ending in `*`, or `*` for everything.
func blockedMatch(entry, op string) bool {
	if prefix, ok := strings.CutSuffix(entry, "*"); ok {
		return strings.HasPrefix(op, prefix)
	}
	return entry == op
}

// PutBlockedOperations replaces the list of every connector the body names;
// a connector it leaves out keeps its list, and an empty list clears it.
func (r *Runtime) PutBlockedOperations(in BlockedOperations) (BlockedOperations, error) {
	norm, err := normalizeBlocked(in)
	if err != nil {
		return BlockedOperations{}, err
	}
	stored, err := r.BlockedOperations()
	if err != nil {
		return BlockedOperations{}, err
	}
	for conn := range in.Connectors {
		if list, ok := norm.Connectors[conn]; ok {
			stored.Connectors[conn] = list
		} else {
			delete(stored.Connectors, conn)
		}
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return BlockedOperations{}, fmt.Errorf("marshal blocked operations: %w", err)
	}
	if err := r.store.SetSetting(blockedOperationsKey, encoded); err != nil {
		return BlockedOperations{}, fmt.Errorf("persist blocked operations: %w", err)
	}
	return stored, nil
}
