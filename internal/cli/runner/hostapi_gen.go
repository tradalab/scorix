package runner

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tradalab/scorix/internal/cli/template"
	"github.com/tradalab/scorix/internal/hostapi"
)

func hostProto(src string) (protoFile, error) {
	pf, err := parseProto(src)
	if err != nil {
		return protoFile{}, err
	}
	if _, _, err := enrichProto(&pf); err != nil {
		return protoFile{}, err
	}
	return pf, nil
}

// The host's own plugins, which an app plugin of the same name replaces (ScorixHost.register).
func hostPlugins() map[string]bool {
	names := map[string]bool{}
	// A host.proto that does not parse fails TestHostAPIIsCurrent before any release.
	pf, _ := hostProto(hostapi.Proto)
	for _, svc := range pf.Natives {
		names[svc.Package] = true
	}
	return names
}

// GenerateHostAPI writes, or with check compares, what host.proto generates in the scorix checkout at root.
func GenerateHostAPI(root string, check bool) error {
	src, err := os.ReadFile(filepath.Join(root, "internal", "hostapi", "host.proto"))
	if err != nil {
		return err
	}
	pf, err := hostProto(string(src))
	if err != nil {
		return fmt.Errorf("host.proto: %w", err)
	}
	kt, err := kotlinNatives(pf, "")
	if err != nil {
		return fmt.Errorf("host.proto: %w", err)
	}
	kt.Host, kt.Vis = true, "internal "
	files := []generatedFile{
		{Path: filepath.Join(root, "internal", "hostapi", "hostapi.go"), Template: mustRead(template.GoHostAPI), Data: pf, Go: true, Force: true},
		{Path: filepath.Join(root, "mobile", "android", "scorix", "src", "main", "kotlin", "dev", "scorix", "HostPlugins.kt"),
			Template: mustRead(template.AndroidPlugins), Data: kt, Force: true},
	}
	for _, f := range files {
		s, err := renderGeneratedFile(f)
		if err != nil {
			return err
		}
		if !s.NeedsWrite {
			continue
		}
		if check {
			return fmt.Errorf("%s does not match host.proto; run go generate ./internal/hostapi", s.Path)
		}
		if err := os.WriteFile(s.Path, s.Content, 0o644); err != nil {
			return err
		}
		fmt.Printf("==> %s %s\n", s.Action, s.Path)
	}
	return nil
}
