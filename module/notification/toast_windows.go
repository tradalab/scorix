//go:build windows

package notification

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"os/exec"
	"strings"
	"unicode/utf16"
)

// Everything here is protocol activation: a click opens a URL the app already
// knows how to receive, instead of a COM class it would have to register and
// keep alive.
func toastXML(req NotifyRequest, scheme string) (string, error) {
	type action struct {
		XMLName        xml.Name `xml:"action"`
		Content        string   `xml:"content,attr"`
		Arguments      string   `xml:"arguments,attr"`
		ActivationType string   `xml:"activationType,attr"`
	}
	type text struct {
		XMLName xml.Name `xml:"text"`
		Value   string   `xml:",chardata"`
	}
	type binding struct {
		XMLName  xml.Name `xml:"binding"`
		Template string   `xml:"template,attr"`
		Texts    []text
	}
	type visual struct {
		XMLName xml.Name `xml:"visual"`
		Binding binding
	}
	type actions struct {
		XMLName xml.Name `xml:"actions"`
		Actions []action
	}
	type toast struct {
		XMLName        xml.Name `xml:"toast"`
		Launch         string   `xml:"launch,attr,omitempty"`
		ActivationType string   `xml:"activationType,attr,omitempty"`
		Visual         visual
		Actions        *actions `xml:",omitempty"`
	}

	t := toast{
		Launch:         activationURL(scheme, req.ID, DefaultAction),
		ActivationType: "protocol",
		Visual: visual{Binding: binding{
			Template: "ToastGeneric",
			Texts:    []text{{Value: req.Title}, {Value: req.Text}},
		}},
	}
	if t.Launch == "" {
		// No id or no scheme: a click has nowhere to go, so do not claim otherwise.
		t.Launch, t.ActivationType = "", ""
	}
	if len(req.Actions) > 0 && t.Launch != "" {
		as := &actions{}
		for _, a := range req.Actions {
			as.Actions = append(as.Actions, action{
				Content:        a.Label,
				Arguments:      activationURL(scheme, req.ID, a.Key),
				ActivationType: "protocol",
			})
		}
		t.Actions = as
	}
	b, err := xml.Marshal(t)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// The AUMID decides whose name and icon the toast carries, and it only resolves
// to the app when the installer stamped System.AppUserModelID on the shortcut.
func powershellToast(aumid, doc string) string {
	var b strings.Builder
	b.WriteString("[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType=WindowsRuntime] > $null;")
	b.WriteString("$x = New-Object Windows.Data.Xml.Dom.XmlDocument;")
	fmt.Fprintf(&b, "$x.LoadXml(%s);", psQuote(doc))
	fmt.Fprintf(&b, "$n = [Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier(%s);", psQuote(aumid))
	b.WriteString("$n.Show([Windows.UI.Notifications.ToastNotification]::new($x));")
	return b.String()
}

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// -EncodedCommand takes base64 UTF-16LE, which sidesteps every layer of quoting
// between here and the shell - the toast XML is full of quotes and ampersands.
func encodeCommand(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, 0, len(u)*2)
	for _, r := range u {
		b = append(b, byte(r), byte(r>>8))
	}
	return base64.StdEncoding.EncodeToString(b)
}

func showToast(ctx context.Context, app appInfo, req NotifyRequest, scheme string) error {
	doc, err := toastXML(req, scheme)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-EncodedCommand", encodeCommand(powershellToast(app.aumid(), doc)))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("toast failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
