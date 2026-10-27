package workbox

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
)

// configStore owns persistence, schema handling and concurrent-write detection.
// snapshot returns a consistent, detached domain snapshot without initializing
// an empty store. commit atomically saves next only if expected still matches;
// stale writes return CONFIG_CONFLICT. commit(s,s) prepares schema without fake
// records. Ambiguous commit acknowledgements return COMMIT_OUTCOME_UNKNOWN.
// Drivers and transactions stay behind this interface.
type configStore interface {
	snapshot(context.Context) (snapshot, *Failure)
	commit(ctx context.Context, expected, next snapshot) *Failure
}

func sameSnapshot(a, b snapshot) bool { return reflect.DeepEqual(a, b) }

func (s snapshot) clone() snapshot {
	copy := emptySnapshot()
	for key, team := range s.Teams {
		copy.Teams[key] = team
	}
	for name, value := range s.Repos {
		value.TeamIDs = append([]string(nil), value.TeamIDs...)
		value.Teams = append([]string(nil), value.Teams...)
		copy.Repos[name] = value
	}
	for name, value := range s.Workspaces {
		value.Repos = append([]StoredRef{}, value.Repos...)
		for i := range value.Repos {
			value.Repos[i].Ref = cloneRef(value.Repos[i].Ref)
		}
		copy.Workspaces[name] = value
	}
	return copy
}

// Runtime composition always enforces the initialization record. Init uses
// sourceStore/candidateStore explicitly to adopt legacy data without a bypass.
func configuredStore() (configStore, *Failure) { return initializedStore() }

// Commit errors cannot in general establish whether the server committed.
func commitFailure(err error) *Failure {
	if errors.Is(err, sql.ErrTxDone) {
		return dbError(err)
	}
	return fail("COMMIT_OUTCOME_UNKNOWN", "Database commit outcome is unknown; reconnect and reread the target before retrying")
}
