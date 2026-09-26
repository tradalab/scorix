package llm

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"
)

// A driver is a protocol; a provider is one configured instance of it. Keeping
// the two apart is what lets a settings screen add a provider from data - pick a
// driver, fill in its fields - with no per-engine code in the app.
type Driver interface {
	Name() string
	Fields() []Field
	Open(cfg ProviderConfig) (Provider, error)
}

type FieldKind string

const (
	FieldText   FieldKind = "text"
	FieldURL    FieldKind = "url"
	FieldSecret FieldKind = "secret"
	FieldBool   FieldKind = "bool"
)

type Field struct {
	Key         string
	Label       string
	Kind        FieldKind
	Required    bool
	Default     string
	Description string
}

// Secret fields never arrive as Values: the driver reads them through Secret on
// every use, so the app can keep them sealed and change them in place.
type ProviderConfig struct {
	ID     string
	Values map[string]string
	Secret func(ctx context.Context, key string) (string, error)
	// Passed through to the driver's retry policy, so whoever opened the
	// provider can see a rate limit being ridden out. A driver that retries has
	// to carry this: a wait nobody records is a wait nobody can explain.
	OnRetry func(attempt int, wait time.Duration, status int, err error)
}

var (
	driversMu sync.RWMutex
	drivers   = map[string]Driver{}
)

// Panics on a second driver under a name, as database/sql does: it is a build
// mistake, and init time is the loudest place to find it.
func RegisterDriver(d Driver) {
	driversMu.Lock()
	defer driversMu.Unlock()
	name := d.Name()
	if name == "" {
		panic("llm: driver with an empty name")
	}
	if _, dup := drivers[name]; dup {
		panic(fmt.Sprintf("llm: driver %q registered twice", name))
	}
	drivers[name] = d
}

func LookupDriver(name string) (Driver, bool) {
	driversMu.RLock()
	defer driversMu.RUnlock()
	d, ok := drivers[name]
	return d, ok
}

// Sorted by name, so a settings screen lists them the same way every launch.
func Drivers() []Driver {
	driversMu.RLock()
	defer driversMu.RUnlock()
	out := make([]Driver, 0, len(drivers))
	for _, name := range slices.Sorted(maps.Keys(drivers)) {
		out = append(out, drivers[name])
	}
	return out
}

// The path a settings screen takes. Defaults and required fields are handled
// here so no driver has to repeat them; a secret's presence cannot be checked
// without reading it, so required-ness covers plain fields only.
func Open(driver string, cfg ProviderConfig) (Provider, error) {
	d, ok := LookupDriver(driver)
	if !ok {
		return nil, fmt.Errorf("llm: no driver named %q", driver)
	}
	values := maps.Clone(cfg.Values)
	if values == nil {
		values = map[string]string{}
	}
	for _, f := range d.Fields() {
		if f.Kind == FieldSecret {
			continue
		}
		if values[f.Key] == "" && f.Default != "" {
			values[f.Key] = f.Default
		}
		if f.Required && values[f.Key] == "" {
			return nil, fmt.Errorf("llm %s: driver %s needs %q", cfg.ID, driver, f.Key)
		}
	}
	cfg.Values = values
	return d.Open(cfg)
}
