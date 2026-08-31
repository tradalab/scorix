package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func buildNSIS(ctx context.Context, bc *BuildContext) (string, error) {
	makensis, err := exec.LookPath("makensis")
	if err != nil {
		return "", fmt.Errorf("makensis not found in PATH - install NSIS (https://nsis.sourceforge.io or `winget install NSIS.NSIS`), or use --format msi")
	}

	if err := signWindows(ctx, bc, filepath.Join(bc.TempDir, bc.BinaryName)); err != nil {
		return "", err
	}

	outDir := filepath.Join(bc.Root, "dist")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(outDir, fmt.Sprintf("%s-%s-setup.exe", bc.ProductName, orDefault(bc.Version, "0.0.0")))

	iconLine := ""
	if bc.IconPath != "" {
		if _, err := os.Stat(bc.IconPath); err == nil {
			iconLine = fmt.Sprintf("Icon %q\n", bc.IconPath)
		}
	}

	script := fmt.Sprintf(`Unicode true
Name %[1]q
OutFile %[2]q
RequestExecutionLevel user
InstallDir "$LOCALAPPDATA\Programs\%[1]s"
%[5]sSection "Install"
  SetOutPath $INSTDIR
  File "/oname=%[3]s" %[4]q
  CreateDirectory "$SMPROGRAMS\%[1]s"
  CreateShortcut "$SMPROGRAMS\%[1]s\%[1]s.lnk" "$INSTDIR\%[3]s"
  WriteUninstaller "$INSTDIR\uninstall.exe"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\%[6]s" "DisplayName" %[1]q
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\%[6]s" "DisplayVersion" %[7]q
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\%[6]s" "Publisher" %[8]q
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\%[6]s" "UninstallString" "$\"$INSTDIR\uninstall.exe$\""
SectionEnd
Section "Uninstall"
  Delete "$INSTDIR\%[3]s"
  Delete "$INSTDIR\uninstall.exe"
  RMDir $INSTDIR
  Delete "$SMPROGRAMS\%[1]s\%[1]s.lnk"
  RMDir "$SMPROGRAMS\%[1]s"
  DeleteRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\%[6]s"
SectionEnd
`, bc.ProductName, out, bc.BinaryName, filepath.Join(bc.TempDir, bc.BinaryName), iconLine, bc.Identifier, orDefault(bc.Version, "0.0.0"), orDefault(bc.Manufacturer, bc.ProductName))

	nsi := filepath.Join(bc.TempDir, "installer.nsi")
	if err := os.WriteFile(nsi, []byte(script), 0o644); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, makensis, nsi)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("makensis: %w", err)
	}
	return out, nil
}
