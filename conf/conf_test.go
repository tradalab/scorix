package conf

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type pool struct {
	MaxLinks int  `json:",default=50"`
	Custom   bool `json:",optional"`
}

type common struct {
	Name string
}

type svcConfig struct {
	common
	ListenOn string        `yaml:"listen_on"`
	Timeout  time.Duration `conf:"default=2s"`
	Token    string        `env:"SVC_TOKEN,secret"`
	Data     string        `env:"SVC_DATA"`
	Tags     []string      `json:",optional"`
	Pool     pool
	Extra    struct {
		Note string
	} `json:",optional"`
	Hidden string `json:"-"`
	Region string `json:"region,default=vn"`
}

func env(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
}

func load(t *testing.T, yml string, vars map[string]string) (svcConfig, error) {
	t.Helper()
	var c svcConfig
	err := loadService("svc.yaml", []byte(yml), &c, env(vars), os.ReadFile)
	return c, err
}

const okYAML = `Name: shorten.rpc
listen_on: 0.0.0.0:8070
Data: from-file
Pool:
  Custom: true
`

func TestAServiceConfigLoadsFileDefaultsAndEnv(t *testing.T) {
	c, err := load(t, okYAML, map[string]string{"SVC_TOKEN": "s3cret", "SVC_DATA": "from-env"})
	if err != nil {
		t.Fatal(err)
	}
	want := svcConfig{common: common{Name: "shorten.rpc"}, ListenOn: "0.0.0.0:8070", Timeout: 2 * time.Second, Token: "s3cret", Data: "from-env", Pool: pool{MaxLinks: 50, Custom: true}}
	if c.Name != want.Name || c.ListenOn != want.ListenOn || c.Timeout != want.Timeout || c.Token != want.Token || c.Data != want.Data || c.Pool != want.Pool || c.Tags != nil {
		t.Fatalf("loaded %+v\nwant   %+v", c, want)
	}
	c, err = load(t, okYAML+"Timeout: 15s\nTags: [a, b]\nregion: sg\n", map[string]string{"SVC_TOKEN": "x"})
	if err != nil || c.Timeout != 15*time.Second || c.Data != "from-file" || len(c.Tags) != 2 || c.Region != "sg" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestEveryRequiredKeyLeftUnsetIsNamed(t *testing.T) {
	_, err := load(t, "Pool: {}\n", nil)
	if err == nil {
		t.Fatal("a config with nothing set loaded")
	}
	for _, key := range []string{"Name", "listen_on", "Token ($SVC_TOKEN)", "Data ($SVC_DATA)"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("%v does not name %s", err, key)
		}
	}
	for _, key := range []string{"Timeout", "Tags", "MaxLinks", "Extra"} {
		if strings.Contains(err.Error(), key) {
			t.Errorf("%v names %s, which is defaulted or optional", err, key)
		}
	}
}

func TestAMissingRequiredSectionStillNamesItsKeys(t *testing.T) {
	type c struct {
		Auth struct {
			Issuer string
		}
	}
	err := loadService("svc.yaml", []byte("{}\n"), &c{}, env(nil), os.ReadFile)
	if err == nil || !strings.Contains(err.Error(), "Auth.Issuer") {
		t.Fatalf("err = %v, want Auth.Issuer named", err)
	}
}

func TestAKeyNoFieldTakesIsAnErrorWithItsLine(t *testing.T) {
	_, err := load(t, okYAML+"  MaxLinx: 3\nHidden: x\n", map[string]string{"SVC_TOKEN": "x"})
	for _, want := range []string{"svc.yaml:6: Pool.MaxLinx is not a field", "svc.yaml:7: Hidden is not a field"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want %q", err, want)
		}
	}
}

func TestNothingInTheFileIsExpanded(t *testing.T) {
	_, err := load(t, okYAML+"Tags: [\"${HOME}\"]\n", map[string]string{"SVC_TOKEN": "x"})
	if err == nil || !strings.Contains(err.Error(), "svc.yaml:6: Tags holds ${...}") {
		t.Fatalf("err = %v", err)
	}
}

func TestASecretInTheFileIsRefused(t *testing.T) {
	_, err := load(t, okYAML+"Token: leaked\n", map[string]string{"SVC_TOKEN": "x"})
	if err == nil || !strings.Contains(err.Error(), "Token is a secret and may come only from $SVC_TOKEN") {
		t.Fatalf("err = %v", err)
	}
}

func TestAnEnvFileOptionReadsTheNamedFile(t *testing.T) {
	type c struct {
		Key string `env:"SVC_KEY_FILE,file"`
	}
	path := t.TempDir() + "/key"
	os.WriteFile(path, []byte("from-file\n"), 0o600)
	var got c
	if err := loadService("svc.yaml", []byte("{}"), &got, env(map[string]string{"SVC_KEY_FILE": path}), os.ReadFile); err != nil || got.Key != "from-file" {
		t.Fatalf("%+v %v", got, err)
	}
	if err := loadService("svc.yaml", []byte("{}"), &got, env(map[string]string{"SVC_KEY_FILE": path + ".gone"}), os.ReadFile); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want the missing file", err)
	}
}

func TestValuesOfTheWrongShapeAreRefused(t *testing.T) {
	for name, c := range map[string]struct{ yml, want string }{
		"int":     {okYAML + "  MaxLinks: lots\n", "svc.yaml:6: Pool.MaxLinks"},
		"mapping": {strings.Replace(okYAML, "Pool:\n  Custom: true\n", "Pool: 3\n", 1), "svc.yaml:4: Pool is not a mapping"},
	} {
		if _, err := load(t, c.yml, map[string]string{"SVC_TOKEN": "x"}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

func TestAnUnknownKeyInsideAListOrMapElementIsAnError(t *testing.T) {
	type host struct {
		Name string `yaml:"name"`
		Port int    `yaml:"port"`
	}
	type c struct {
		Hosts  []host          `yaml:"hosts"`
		Routes map[string]host `yaml:"routes"`
	}
	good := "hosts: [{name: a, port: 80}]\nroutes: {x: {name: b, port: 81}}\n"
	var got c
	if err := loadService("svc.yaml", []byte(good), &got, env(nil), os.ReadFile); err != nil || got.Hosts[0].Port != 80 || got.Routes["x"].Port != 81 {
		t.Fatalf("%+v %v", got, err)
	}
	for _, yml := range []string{
		"hosts: [{name: a, prot: 80}]\nroutes: {}\n",
		"hosts: []\nroutes: {x: {name: b, prot: 81}}\n",
	} {
		if err := loadService("svc.yaml", []byte(yml), &c{}, env(nil), os.ReadFile); err == nil || !strings.Contains(err.Error(), "field prot not found") {
			t.Errorf("%q: err = %v, want the unknown key named", yml, err)
		}
	}
}

func TestAnEmptyValueLeavesAKeyUnset(t *testing.T) {
	c, err := load(t, okYAML+"Timeout:\n", map[string]string{"SVC_TOKEN": "x"})
	if err != nil || c.Timeout != 2*time.Second {
		t.Fatalf("a null defaulted key = %v, %v; want its default", c.Timeout, err)
	}
	_, err = load(t, strings.Replace(okYAML, "listen_on: 0.0.0.0:8070", "listen_on:", 1), map[string]string{"SVC_TOKEN": "", "SVC_DATA": ""})
	for _, key := range []string{"listen_on", "Token ($SVC_TOKEN)"} {
		if err == nil || !strings.Contains(err.Error(), "required keys unset") || !strings.Contains(err.Error(), key) {
			t.Errorf("err = %v, want %s named as unset", err, key)
		}
	}
	type opt struct {
		Note  string        `env:"SVC_NOTE" json:",optional"`
		Wait  time.Duration `env:"SVC_WAIT" conf:"default=3s"`
		Token string        `env:"SVC_TOKEN"`
	}
	var got opt
	if err := loadService("svc.yaml", []byte("Note: from-file\n"), &got, env(map[string]string{"SVC_NOTE": "", "SVC_WAIT": "", "SVC_TOKEN": "t"}), os.ReadFile); err != nil || got.Note != "" || got.Wait != 3*time.Second {
		t.Fatalf("an empty env value clears an optional key and leaves a defaulted one to its default: %+v %v", got, err)
	}
}

func TestStructsThatCannotBeConfigAreRefused(t *testing.T) {
	type withPtr struct {
		P *string
	}
	if err := loadService("svc.yaml", []byte("P: x\n"), &withPtr{}, env(nil), os.ReadFile); err == nil || !strings.Contains(err.Error(), "P is a ptr") {
		t.Errorf("pointer field: %v", err)
	}
	type withRange struct {
		N int `json:",range=[1:5]"`
	}
	if err := loadService("svc.yaml", []byte("N: 2\n"), &withRange{}, env(nil), os.ReadFile); err == nil || !strings.Contains(err.Error(), `tag option "range=[1:5]" is not read here`) {
		t.Errorf("go-zero range option: %v", err)
	}
	n := 0
	for _, dst := range []any{svcConfig{}, (*svcConfig)(nil), &n} {
		if err := loadService("svc.yaml", []byte("{}"), dst, env(nil), os.ReadFile); err == nil || !strings.Contains(err.Error(), "Load needs a pointer to a struct") {
			t.Errorf("destination %T: %v", dst, err)
		}
	}
}
