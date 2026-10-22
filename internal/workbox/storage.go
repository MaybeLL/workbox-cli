package workbox

import (
	"context"
	"os"
	"reflect"
)

// configStore owns persistence, schema handling and concurrent-write detection.
// snapshot returns a consistent, detached domain snapshot without initializing
// an empty store. commit atomically saves next only if expected still matches;
// stale writes return CONFIG_CONFLICT. Drivers and transactions stay behind it.
type configStore interface {
	snapshot(context.Context) (snapshot, *Failure)
	commit(ctx context.Context, expected, next snapshot) *Failure
}

func sameSnapshot(a, b snapshot) bool { return reflect.DeepEqual(a, b) }

func (s snapshot) clone() snapshot {
	copy := emptySnapshot()
	for name, value := range s.Repos {
		copy.Repos[name] = value
	}
	for name, value := range s.Workspaces {
		value.Repos = append([]StoredRef{}, value.Repos...)
		copy.Workspaces[name] = value
	}
	return copy
}

// Backend selection is confined to command composition; business operations
// depend only on configStore. Status, skill and uninstall never call this.
func configuredStore() (configStore, *Failure) {
	switch os.Getenv("WORKBOX_DB") {
	case "", "sqlite":
		path, e := configPath()
		if e != nil {
			return nil, e
		}
		return &sqliteStore{path: path}, nil
	case "mysql":
		return configuredMySQLStore()
	default:
		return nil, invalid("WORKBOX_DB must be sqlite or mysql")
	}
}
