// Package llm is scorix/llm as a module: the providers the user added, their
// keys sealed, the model each of the app's slots uses, and model downloads
// with progress, all reachable from the frontend as mod:llm:*. The app declares
// its slots and calls models through Registry; what the user chose is kept in
// <DataDir>/llm.json.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tradalab/scorix/fault"
	core "github.com/tradalab/scorix/llm"
	"github.com/tradalab/scorix/llm/detect"
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
	"github.com/tradalab/scorix/secrets"

	// The drivers a user can add from the settings screen.
	_ "github.com/tradalab/scorix/llm/anthropic"
	_ "github.com/tradalab/scorix/llm/ollama"
	_ "github.com/tradalab/scorix/llm/openai"
)

type Slot struct {
	Name        string            `json:"name"`
	Label       string            `json:"label"`
	Description string            `json:"description,omitempty"`
	Needs       []core.Capability `json:"needs"`
	LocalOnly   bool              `json:"local_only"`
}

type Options struct {
	Slots []Slot
	// Seals API keys. Nil opens the OS credential store under the app
	// identifier the first time a key is saved or read.
	Secrets *secrets.Store
	// <DataDir>/llm.json when empty.
	Path string
	// Asks the person at the machine, natively. Required for DeleteModel and
	// DeleteProvider, which destroy what no download brings back; nil refuses
	// both. A dialog the page draws proves nothing - see module/llm/confirm.
	Confirm func(ctx context.Context, question string) (bool, error)
}

// Read from the manifest section of this module: modules.llm.*.
type Config struct {
	// Globs over "<provider>/<model>". Empty means none: Pull writes gigabytes
	// under a name the page chose, so an app says where that may come from.
	PullAllow []string `json:"pull_allow"`
}

// The names an app writes in security.allowlist, split by what damage each
// one does rather than by read and write.
const (
	PermUse       module.Permission = "llm:use"
	PermProviders module.Permission = "llm:providers:write"
	PermModels    module.Permission = "llm:models:write"
	// What every manifest written before permissions existed uses.
	PermAll      module.Permission = "llm"
	PermReadOnly module.Permission = "llm:readonly"
)

func (m *Module) Permissions() module.PermissionSet {
	return module.PermissionSet{
		Atoms: []module.Permission{PermUse, PermProviders, PermModels},
		Sets: map[module.Permission][]module.Permission{
			PermAll:      {PermUse, PermProviders, PermModels},
			PermReadOnly: {PermUse},
		},
	}
}

type Module struct {
	opt   Options
	cfg   Config
	reg   *core.Registry
	ipc   *module.ModuleIPC
	appID string
	path  string

	mu      sync.Mutex
	saved   saved
	openErr map[string]string
	app     map[string]bool
	pulls   map[string]context.CancelFunc
	// A file from a newer build: read, never written.
	frozen   bool
	detected func(context.Context) []detect.Found

	// Its own lock: opening the credential store can put a dialog on screen,
	// and that must not sit under mu.
	sealMu    sync.Mutex
	sealer    *secrets.Store
	sealTried time.Time
	sealErr   error
}

type saved struct {
	// Missing is a file written before this field, which is shape 1. A number
	// this build does not know is a file from a newer one.
	Version   int                     `json:"version"`
	Providers []savedProvider         `json:"providers"`
	Bindings  map[string]savedBinding `json:"bindings"`
}

const savedVersion = 1

type savedProvider struct {
	ID     string            `json:"id"`
	Driver string            `json:"driver"`
	Values map[string]string `json:"values,omitempty"`
	// Sealed with scorix/secrets; never sent to the frontend.
	Secrets map[string]string `json:"secrets,omitempty"`
}

type savedBinding struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func New(opt Options) *Module {
	return &Module{
		opt: opt, reg: core.NewRegistry(), sealer: opt.Secrets,
		openErr: map[string]string{}, app: map[string]bool{}, pulls: map[string]context.CancelFunc{},
		saved:    saved{Bindings: map[string]savedBinding{}},
		detected: detect.Local,
	}
}

func (m *Module) Name() string    { return "llm" }
func (m *Module) Version() string { return "1.0.0" }

func (m *Module) Capability() string { return "llm" }

func (m *Module) Registry() *core.Registry { return m.reg }

func (m *Module) OnLoad(ctx *module.Context) error {
	m.ipc, m.appID = ctx.IPC, ctx.AppIdentifier
	if err := ctx.Decode(&m.cfg); err != nil {
		return fmt.Errorf("llm: modules.llm: %w", err)
	}
	m.path = m.opt.Path
	if m.path == "" {
		m.path = filepath.Join(ctx.DataDir, "llm.json")
	}
	if !filepath.IsAbs(m.path) {
		// An empty DataDir joins to a bare name, and every sealed key would land
		// in whatever directory the app was started from.
		return fmt.Errorf("llm: %q is not an absolute path; the app has no data directory", m.path)
	}
	if err := m.load(); err != nil {
		return err
	}
	// Grouped by what a page could do with it: Bind writes a setting, Pull
	// writes gigabytes.
	for needs, names := range map[module.Permission][]string{
		PermUse: {"Drivers", "State", "Models", "Describe"},
		// Detect knocks on this machine's ports looking for runtimes, which is
		// how a provider gets added, not part of using one.
		PermProviders: {"SaveProvider", "DeleteProvider", "Bind", "Unbind", "Detect"},
		PermModels:    {"Pull", "CancelPull", "DeleteModel"},
	} {
		for _, name := range names {
			module.Expose(m, name, ctx.IPC, module.Needs(needs))
		}
	}
	return nil
}

func (m *Module) OnStart() error { return nil }

func (m *Module) OnStop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, cancel := range m.pulls {
		cancel()
	}
	return nil
}

func (m *Module) OnUnload() error { return nil }

// A saved provider that no longer opens stays in the file and on screen with
// its error, so the user can fix it instead of finding it gone.
func (m *Module) load() error {
	// Before the read: the first save is the likeliest to be interrupted, and
	// the ErrNotExist return below would leave its temp file for good.
	m.sweepTemps()
	b, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("llm: %w", err)
	}
	if err := json.Unmarshal(b, &m.saved); err != nil {
		// Failing here would stop the app from starting over a settings file;
		// starting empty would overwrite it on the next save. Set it aside.
		aside := fmt.Sprintf("%s.corrupt-%d", m.path, time.Now().Unix())
		logger.Warn(fmt.Sprintf("[llm] %s is unreadable (%v); moved to %s", m.path, err, aside))
		m.saved = saved{}
		if err := os.Rename(m.path, aside); err != nil {
			return fmt.Errorf("llm: set aside %s: %w", m.path, err)
		}
	}
	if m.saved.Version > savedVersion {
		// Read what can be read, change nothing: saving over it would drop the
		// providers and keys a newer build added.
		logger.Warn(fmt.Sprintf("[llm] %s was written by a newer version (%d); settings are read-only until that version runs again", m.path, m.saved.Version))
		m.frozen = true
	}
	if m.saved.Bindings == nil {
		m.saved.Bindings = map[string]savedBinding{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sp := range m.saved.Providers {
		// The app registered this ID before the file was read, so Register could
		// not see the record. Opening it now would replace the app's provider
		// while State still lists it as the app's.
		if m.app[sp.ID] {
			m.openErr[sp.ID] = fmt.Sprintf("%q is run by the app", sp.ID)
			logger.Warn(fmt.Sprintf("[llm] saved provider %s is also run by the app; the app's own is used", sp.ID))
			continue
		}
		if err := m.open(sp); err != nil {
			m.openErr[sp.ID] = err.Error()
			logger.Warn(fmt.Sprintf("[llm] provider %s: %v", sp.ID, err))
		}
	}
	m.rebind()
	return nil
}

// For providers the app runs itself, such as a llama-server it started. They
// are listed but not editable, and saved bindings that name them apply now.
func (m *Module) Register(p core.Provider) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if slices.ContainsFunc(m.saved.Providers, func(sp savedProvider) bool { return sp.ID == p.ID() }) {
		return fmt.Errorf("llm: %q is a provider the user added", p.ID())
	}
	if err := m.reg.Register(p); err != nil {
		return err
	}
	m.app[p.ID()] = true
	m.rebind()
	return nil
}

func (m *Module) Unregister(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.app[id] {
		return
	}
	m.reg.Unregister(id)
	delete(m.app, id)
	delete(m.openErr, id)
	// The app has let the ID go, so a record saved under it is the answer now.
	if i := slices.IndexFunc(m.saved.Providers, func(sp savedProvider) bool { return sp.ID == id }); i >= 0 {
		if err := m.open(m.saved.Providers[i]); err != nil {
			m.openErr[id] = err.Error()
		}
		m.resync()
	}
}

func (m *Module) open(sp savedProvider) error {
	id := sp.ID
	// Open runs under mu and a driver may read its key there to validate it,
	// so until Open returns the key comes from the record being opened.
	var opened atomic.Bool
	p, err := core.Open(sp.Driver, core.ProviderConfig{ID: id, Values: sp.Values, Secret: func(ctx context.Context, key string) (string, error) {
		if opened.Load() {
			return m.secret(id, key)
		}
		return m.unseal(sp.Secrets[key])
	}, OnRetry: func(attempt int, wait time.Duration, status int, err error) {
		// The only record that a request was ridden out rather than answered.
		logger.Info("[llm] retrying", "provider", id, "attempt", attempt, "wait", wait.String(), "status", status, "error", err)
	}})
	opened.Store(true)
	if err != nil {
		return err
	}
	m.reg.Unregister(id)
	return m.reg.Register(p)
}

// Read on every request, so a key changed in settings takes effect on the
// next one without reopening anything.
func (m *Module) secret(id, key string) (string, error) {
	m.mu.Lock()
	var sealed string
	for _, sp := range m.saved.Providers {
		if sp.ID == id {
			sealed = sp.Secrets[key]
		}
	}
	m.mu.Unlock()
	return m.unseal(sealed)
}

func (m *Module) unseal(sealed string) (string, error) {
	// A value that was never sealed reads back as itself: an older file keeps
	// working, and a machine with no credential store is not asked for one.
	if sealed == "" || !secrets.IsEncrypted(sealed) {
		return sealed, nil
	}
	s, err := m.store()
	if err != nil {
		return "", err
	}
	return s.DecryptString(sealed)
}

func (m *Module) store() (*secrets.Store, error) {
	m.sealMu.Lock()
	defer m.sealMu.Unlock()
	if m.sealer != nil {
		return m.sealer, nil
	}
	// The failure is remembered for a while: a machine with no credential
	// store would otherwise be asked on every single request.
	if time.Since(m.sealTried) < storeRetryAfter {
		return nil, m.sealErr
	}
	s, err := secrets.Open(m.appID)
	if err != nil {
		m.sealTried, m.sealErr = time.Now(), fmt.Errorf("llm: keys cannot be sealed on this machine: %w", err)
		return nil, m.sealErr
	}
	m.sealer = s
	return s, nil
}

const storeRetryAfter = time.Minute

func (m *Module) forgetSealFailure() {
	m.sealMu.Lock()
	m.sealTried, m.sealErr = time.Time{}, nil
	m.sealMu.Unlock()
}

// Applies the saved binding of every declared slot. One that fails keeps its
// entry, and State reports why.
func (m *Module) rebind() {
	for _, s := range m.opt.Slots {
		b, ok := m.saved.Bindings[s.Name]
		if !ok {
			continue
		}
		_ = m.reg.Bind(s.Name, core.Binding{Provider: b.Provider, Model: b.Model, LocalOnly: s.LocalOnly})
	}
}

// rebind can only add, so undoing a change that bound a slot needs every
// slot cleared first.
func (m *Module) resync() {
	for _, sl := range m.opt.Slots {
		m.reg.Unbind(sl.Name)
	}
	m.rebind()
}

// A save killed between the temp file and the rename leaves a full copy of
// the sealed keys behind, and nothing else removes it.
func (m *Module) sweepTemps() {
	// ReadDir and not Glob: a data directory with a bracket in its name is a
	// pattern syntax error, and the leak comes back with nothing to see.
	dir, base := filepath.Split(m.path)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-time.Hour)
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, base+".") || !strings.HasSuffix(n, tempSuffix) {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
}

// The tail of the name save() writes through. The sweep above looks for the
// same one.
const tempSuffix = ".tmp"

func (m *Module) save() error {
	if m.frozen {
		return fmt.Errorf("%s was written by a newer version of this app; it is left as it is", m.path)
	}
	m.saved.Version = savedVersion
	b, err := json.MarshalIndent(m.saved, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	// A name of its own per write: the rename is atomic, a shared .tmp is not.
	f, err := os.CreateTemp(filepath.Dir(m.path), filepath.Base(m.path)+".*"+tempSuffix)
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // already gone once the rename lands
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	// Or the rename lands before the bytes do, and a machine that loses power
	// comes back to an empty llm.json with every provider and key gone.
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

type DriverInfo struct {
	Name   string      `json:"name"`
	Fields []FieldInfo `json:"fields"`
}

type FieldInfo struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Kind        string `json:"kind"`
	Required    bool   `json:"required"`
	Default     string `json:"default,omitempty"`
	Description string `json:"description,omitempty"`
}

func (m *Module) Drivers() ([]DriverInfo, error) {
	var out []DriverInfo
	for _, d := range core.Drivers() {
		info := DriverInfo{Name: d.Name(), Fields: []FieldInfo{}}
		for _, f := range d.Fields() {
			info.Fields = append(info.Fields, FieldInfo{Key: f.Key, Label: f.Label, Kind: string(f.Kind), Required: f.Required, Default: f.Default, Description: f.Description})
		}
		out = append(out, info)
	}
	return out, nil
}

type State struct {
	Providers []ProviderState `json:"providers"`
	Slots     []SlotState     `json:"slots"`
	// Nothing can be saved: the file was written by a newer version. A screen
	// that does not know says it once instead of refusing every click.
	ReadOnly bool `json:"read_only"`
}

type ProviderState struct {
	ID     string            `json:"id"`
	Driver string            `json:"driver,omitempty"`
	Values map[string]string `json:"values"`
	// The secret fields that hold a value; the values stay here.
	Secrets      []string          `json:"secrets"`
	Capabilities []core.Capability `json:"capabilities"`
	Local        bool              `json:"local"`
	Manages      bool              `json:"manages"`
	// False for a provider the app registered itself.
	Editable bool   `json:"editable"`
	Error    string `json:"error,omitempty"`
}

type SlotState struct {
	Slot
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (m *Module) State() (*State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state(), nil
}

func (m *Module) state() *State {
	st := &State{Providers: []ProviderState{}, Slots: []SlotState{}, ReadOnly: m.frozen}
	for _, sp := range m.saved.Providers {
		// Shadowed by the app's own provider of that ID, which the loop below
		// lists: two rows with one ID behave as one thing.
		if m.app[sp.ID] {
			continue
		}
		ps := ProviderState{ID: sp.ID, Driver: sp.Driver, Values: sp.Values, Secrets: []string{}, Capabilities: []core.Capability{}, Editable: true, Error: m.openErr[sp.ID]}
		if ps.Values == nil {
			ps.Values = map[string]string{}
		}
		for k := range sp.Secrets {
			ps.Secrets = append(ps.Secrets, k)
		}
		slices.Sort(ps.Secrets)
		if p, ok := m.reg.Provider(sp.ID); ok {
			describe(&ps, p)
		}
		st.Providers = append(st.Providers, ps)
	}
	for _, p := range m.reg.Providers() {
		if m.app[p.ID()] {
			ps := ProviderState{ID: p.ID(), Values: map[string]string{}, Secrets: []string{}}
			describe(&ps, p)
			st.Providers = append(st.Providers, ps)
		}
	}
	for _, s := range m.opt.Slots {
		ss := SlotState{Slot: s}
		if ss.Needs == nil {
			ss.Needs = []core.Capability{}
		}
		if b, ok := m.saved.Bindings[s.Name]; ok {
			ss.Provider, ss.Model = b.Provider, b.Model
			ss.Error = m.slotError(s, b)
		}
		st.Slots = append(st.Slots, ss)
	}
	return st
}

func describe(ps *ProviderState, p core.Provider) {
	ps.Capabilities = append([]core.Capability{}, p.Capabilities()...)
	ps.Local = core.IsLocal(p)
	_, ps.Manages = p.(core.ModelManager)
}

func (m *Module) slotError(s Slot, b savedBinding) string {
	p, ok := m.reg.Provider(b.Provider)
	if !ok {
		if _, broken := m.openErr[b.Provider]; broken {
			return fmt.Sprintf("provider %q does not open", b.Provider)
		}
		return fmt.Sprintf("provider %q is not there", b.Provider)
	}
	if err := fits(s, p); err != nil {
		return err.Error()
	}
	return ""
}

func fits(s Slot, p core.Provider) error {
	for _, c := range s.Needs {
		if !core.Has(p, c) {
			return fmt.Errorf("%q cannot %s: %w", p.ID(), c, core.ErrUnsupported)
		}
	}
	if s.LocalOnly && !core.IsLocal(p) {
		return fmt.Errorf("%q does not run on this machine: %w", p.ID(), core.ErrNotLocal)
	}
	return nil
}

type ProviderInput struct {
	ID     string            `json:"id"`
	Driver string            `json:"driver"`
	Values map[string]string `json:"values"`
	// Only the secret fields being changed: one left out keeps its sealed
	// value, and an empty string clears it.
	Secrets map[string]string `json:"secrets"`
}

var providerID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Refuses anything that does not open, so a broken provider is never saved
// from the settings screen: the error is shown while the user is still there.
func (m *Module) SaveProvider(in ProviderInput) (*State, error) {
	if !providerID.MatchString(in.ID) {
		return nil, fmt.Errorf("llm: %q is not a provider ID: lowercase letters, digits, dot, dash, underscore", in.ID)
	}
	d, ok := core.LookupDriver(in.Driver)
	if !ok {
		return nil, fmt.Errorf("llm: no driver named %q", in.Driver)
	}
	// A save the user asked for is not the storm store() remembers: someone
	// who cancelled a keychain prompt must get it again.
	m.forgetSealFailure()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.app[in.ID] {
		return nil, fmt.Errorf("llm: %q is run by the app and cannot be edited", in.ID)
	}
	i := slices.IndexFunc(m.saved.Providers, func(sp savedProvider) bool { return sp.ID == in.ID })
	var prev savedProvider
	if i >= 0 {
		prev = m.saved.Providers[i]
	}
	if i >= 0 && prev.Driver != in.Driver {
		return nil, fmt.Errorf("llm: %q already uses the %s driver; remove it to change that", in.ID, prev.Driver)
	}
	next := savedProvider{ID: in.ID, Driver: in.Driver, Values: map[string]string{}, Secrets: map[string]string{}}
	for k, v := range prev.Secrets {
		// secrets takes a value that was never sealed, so a key hand-edited into
		// the file works and reads the same on screen. Sealed on the next save,
		// not at startup, which would hold the app behind an unlock dialog.
		if !secrets.IsEncrypted(v) {
			if st, err := m.store(); err == nil {
				if sealed, err := st.EncryptString(v); err == nil {
					v = sealed
				}
			}
		}
		next.Secrets[k] = v
	}
	for _, f := range d.Fields() {
		if f.Kind != core.FieldSecret {
			if v := in.Values[f.Key]; v != "" {
				next.Values[f.Key] = v
			}
			continue
		}
		v, changed := in.Secrets[f.Key]
		switch {
		case !changed:
		case v == "":
			delete(next.Secrets, f.Key)
		default:
			s, err := m.store()
			if err != nil {
				return nil, err
			}
			sealed, err := s.EncryptString(v)
			if err != nil {
				return nil, fmt.Errorf("llm: seal %s: %w", f.Key, err)
			}
			next.Secrets[f.Key] = sealed
		}
	}
	was, wasRegistered := m.reg.Provider(in.ID)
	wasErr, hadErr := m.openErr[in.ID]
	wasList := slices.Clone(m.saved.Providers)
	// The provider as it was stays registered until this one opens.
	if err := m.open(next); err != nil {
		return nil, err
	}
	if i >= 0 {
		m.saved.Providers[i] = next
	} else {
		m.saved.Providers = append(m.saved.Providers, next)
	}
	delete(m.openErr, in.ID)
	m.rebind()
	if err := m.commit(func() {
		m.saved.Providers = wasList
		m.reg.Unregister(in.ID)
		if wasRegistered {
			_ = m.reg.Register(was)
		}
		if hadErr {
			m.openErr[in.ID] = wasErr
		}
		m.resync()
	}); err != nil {
		return nil, err
	}
	return m.state(), nil
}

// A change the file refused must not stay live: the screen told the user
// it was not saved.
func (m *Module) commit(undo func()) error {
	if err := m.save(); err != nil {
		undo()
		return fmt.Errorf("llm: save: %w", err)
	}
	return nil
}

// Slots bound to it keep the binding and show why it no longer works, rather
// than silently falling back to nothing.
func (m *Module) DeleteProvider(ctx context.Context, id string) (*State, error) {
	if err := m.consent(ctx, fmt.Sprintf("Delete the provider %q? Its saved API key is destroyed with it and cannot be recovered.", id)); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	i := slices.IndexFunc(m.saved.Providers, func(sp savedProvider) bool { return sp.ID == id })
	if i < 0 {
		return nil, fmt.Errorf("llm: no provider %q the user added", id)
	}
	was, wasRegistered := m.reg.Provider(id)
	wasErr, hadErr := m.openErr[id]
	wasList := slices.Clone(m.saved.Providers)
	m.saved.Providers = slices.Delete(m.saved.Providers, i, i+1)
	m.reg.Unregister(id)
	delete(m.openErr, id)
	if err := m.commit(func() {
		m.saved.Providers = wasList
		if wasRegistered {
			_ = m.reg.Register(was)
		}
		if hadErr {
			m.openErr[id] = wasErr
		}
		m.resync()
	}); err != nil {
		return nil, err
	}
	return m.state(), nil
}

type BindInput struct {
	Slot     string `json:"slot"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func (m *Module) Bind(in BindInput) (*State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := slices.IndexFunc(m.opt.Slots, func(s Slot) bool { return s.Name == in.Slot })
	if i < 0 {
		return nil, fmt.Errorf("llm: the app has no slot %q", in.Slot)
	}
	s := m.opt.Slots[i]
	p, ok := m.reg.Provider(in.Provider)
	if !ok {
		return nil, fmt.Errorf("%w: %q", core.ErrUnknownProvider, in.Provider)
	}
	if err := fits(s, p); err != nil {
		return nil, fmt.Errorf("llm: slot %q: %w", s.Name, err)
	}
	if err := m.reg.Bind(s.Name, core.Binding{Provider: in.Provider, Model: in.Model, LocalOnly: s.LocalOnly}); err != nil {
		return nil, err
	}
	was, had := m.saved.Bindings[s.Name]
	m.saved.Bindings[s.Name] = savedBinding{Provider: in.Provider, Model: in.Model}
	if err := m.commit(func() { m.restore(s.Name, was, had) }); err != nil {
		return nil, err
	}
	return m.state(), nil
}

func (m *Module) restore(slot string, was savedBinding, had bool) {
	if had {
		m.saved.Bindings[slot] = was
	} else {
		delete(m.saved.Bindings, slot)
	}
	m.resync()
}

func (m *Module) Unbind(slot string) (*State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	was, had := m.saved.Bindings[slot]
	m.reg.Unbind(slot)
	delete(m.saved.Bindings, slot)
	if err := m.commit(func() { m.restore(slot, was, had) }); err != nil {
		return nil, err
	}
	return m.state(), nil
}

type ModelView struct {
	ID            string `json:"id"`
	Size          int64  `json:"size,omitempty"`
	Family        string `json:"family,omitempty"`
	ParameterSize string `json:"parameter_size,omitempty"`
	Quantization  string `json:"quantization,omitempty"`
}

func (m *Module) Models(ctx context.Context, provider string) ([]ModelView, error) {
	p, ok := m.reg.Provider(provider)
	if !ok {
		return nil, fmt.Errorf("%w: %q", core.ErrUnknownProvider, provider)
	}
	l, ok := p.(core.ModelLister)
	if !ok {
		return nil, fmt.Errorf("llm: %q cannot list its models: %w", provider, core.ErrUnsupported)
	}
	ms, err := l.Models(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ModelView, 0, len(ms))
	for _, mi := range ms {
		out = append(out, ModelView{ID: mi.ID, Size: mi.Size, Family: mi.Family, ParameterSize: mi.ParameterSize, Quantization: mi.Quantization})
	}
	return out, nil
}

type ModelInput struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// Enough that a second one can start while the first waits on the network,
// few enough that they do not starve each other.
const maxPulls = 2

type PullEvent struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Status   string `json:"status"`
	// The layer this line is about: a model comes in several, each counting
	// from zero, so without it the bar runs to the end and drops back.
	Digest string `json:"digest,omitempty"`
	Done   int64  `json:"done"`
	Total  int64  `json:"total"`
}

// Returns when the download ends; progress arrives as mod:llm:pull.
func (m *Module) Pull(ctx context.Context, in ModelInput) (bool, error) {
	if !m.pullAllowed(in.Provider, in.Model) {
		return false, fmt.Errorf("llm: %s/%s is outside modules.llm.pull_allow", in.Provider, in.Model)
	}
	mm, err := m.manager(in.Provider)
	if err != nil {
		return false, err
	}
	key := in.Provider + "\x00" + in.Model
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m.mu.Lock()
	if _, busy := m.pulls[key]; busy {
		m.mu.Unlock()
		return false, fmt.Errorf("llm: %s is already downloading %s", in.Provider, in.Model)
	}
	// Models are gigabytes each: ten at once is ten part files growing and a
	// disk that fills with none of them finished.
	if len(m.pulls) >= maxPulls {
		m.mu.Unlock()
		return false, fmt.Errorf("llm: %d downloads are already running; wait for one to finish", len(m.pulls))
	}
	m.pulls[key] = cancel
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.pulls, key)
		m.mu.Unlock()
	}()
	// Detached from ctx: the event must go out even as ctx is being cancelled.
	emit := func(ev PullEvent) {
		if m.ipc != nil {
			_ = m.ipc.EmitEvent(context.Background(), "pull", ev)
		}
	}
	var last time.Time
	err = mm.Pull(ctx, in.Model, func(p core.PullProgress) {
		// A runtime can report far more often than a screen redraws.
		if time.Since(last) < 100*time.Millisecond && p.Done != p.Total {
			return
		}
		last = time.Now()
		emit(PullEvent{Provider: in.Provider, Model: in.Model, Status: p.Status, Digest: p.Digest, Done: p.Done, Total: p.Total})
	})
	// Stopped by CancelPull: what the user asked for, not a failure.
	if errors.Is(err, context.Canceled) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (m *Module) running() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pulls)
}

func (m *Module) CancelPull(in ModelInput) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cancel, ok := m.pulls[in.Provider+"\x00"+in.Model]
	if ok {
		cancel()
	}
	return ok, nil
}

func (m *Module) DeleteModel(ctx context.Context, in ModelInput) (bool, error) {
	if err := m.consent(ctx, fmt.Sprintf("Delete the model %q from %s? The files are removed from this machine and would have to be downloaded again.", in.Model, in.Provider)); err != nil {
		return false, err
	}
	mm, err := m.manager(in.Provider)
	if err != nil {
		return false, err
	}
	if err := mm.Delete(ctx, in.Model); err != nil {
		return false, err
	}
	return true, nil
}

func (m *Module) Describe(ctx context.Context, in ModelInput) (*core.ModelDetail, error) {
	p, ok := m.reg.Provider(in.Provider)
	if !ok {
		return nil, fmt.Errorf("%w: %q", core.ErrUnknownProvider, in.Provider)
	}
	d, ok := p.(core.Describer)
	if !ok {
		// What the provider says it can do, rather than an error: a frontend reads
		// a failure as "no" and the attach button disappears.
		return &core.ModelDetail{ID: in.Model, Capabilities: slices.Clone(p.Capabilities())}, nil
	}
	return d.Describe(ctx, in.Model)
}

// Globs over "<provider>/<model>", matched whole. A page chooses both halves,
// so both are in what the manifest bounds.
func (m *Module) pullAllowed(provider, model string) bool {
	ref := provider + "/" + model
	for _, pattern := range m.cfg.PullAllow {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if globMatch(pattern, ref) {
			return true
		}
	}
	return false
}

// Only "*", crossing separators: a model reference is not a path.
func globMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		i := strings.Index(s, part)
		if i < 0 {
			return false
		}
		s = s[i+len(part):]
	}
	last := parts[len(parts)-1]
	return strings.HasSuffix(s, last) && len(s) >= len(last)
}

// Refused rather than done quietly when the app wired no way to ask.
func (m *Module) consent(ctx context.Context, question string) error {
	if m.opt.Confirm == nil {
		return errors.New("llm: this app wired no way to confirm a destructive change (llm.Options.Confirm); refusing")
	}
	ok, err := m.opt.Confirm(ctx, question)
	if err != nil {
		return fmt.Errorf("llm: asking for confirmation: %w", err)
	}
	if !ok {
		// A code, not a sentence: matching on wording breaks when it changes.
		return fault.New(fault.CodeCanceled, "llm: cancelled")
	}
	return nil
}

func (m *Module) manager(provider string) (core.ModelManager, error) {
	p, ok := m.reg.Provider(provider)
	if !ok {
		return nil, fmt.Errorf("%w: %q", core.ErrUnknownProvider, provider)
	}
	mm, ok := p.(core.ModelManager)
	if !ok {
		return nil, fmt.Errorf("llm: %q does not manage its models: %w", provider, core.ErrUnsupported)
	}
	return mm, nil
}

type Found struct {
	Name    string `json:"name"`
	Driver  string `json:"driver"`
	BaseURL string `json:"base_url"`
	Version string `json:"version,omitempty"`
}

func (m *Module) Detect(ctx context.Context) ([]Found, error) {
	fs := m.detected(ctx)
	out := make([]Found, 0, len(fs))
	for _, f := range fs {
		out = append(out, Found{Name: f.Name, Driver: f.Driver, BaseURL: f.BaseURL, Version: f.Version})
	}
	return out, nil
}
