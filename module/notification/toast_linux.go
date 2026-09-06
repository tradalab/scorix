//go:build linux

package notification

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/tradalab/scorix/logger"
)

// --wait is what makes a click reportable: notify-send stays alive until the
// notification closes, then prints the chosen action key. Without it the process
// exits at once and the click has nobody to tell.
func notifySendArgs(appName string, req NotifyRequest) []string {
	args := []string{"--app-name=" + appName, "--wait"}
	switch req.Level {
	case "warning":
		args = append(args, "--urgency=normal", "--icon=dialog-warning")
	case "error":
		args = append(args, "--urgency=critical", "--icon=dialog-error")
	default:
		args = append(args, "--urgency=low", "--icon=dialog-information")
	}
	if req.ID != "" {
		// "default" is libnotify's reserved key for a click on the body itself.
		args = append(args, "--action="+DefaultAction+"=Open")
		for _, a := range req.Actions {
			args = append(args, "--action="+a.Key+"="+a.Label)
		}
	}
	return append(args, req.Title, req.Text)
}

// showToast returns once the notification is POSTED, not once it is answered:
// --wait keeps notify-send alive until the user dismisses it, and blocking the
// IPC call for that long would hang the caller behind a toast nobody clicked.
// Start reports a missing notify-send immediately; the click arrives later.
func showToast(ctx context.Context, app appInfo, req NotifyRequest, _ string) error {
	cmd := exec.CommandContext(ctx, "notify-send", notifySendArgs(app.display(), req)...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("notify-send: %w", err)
	}
	go func() {
		b, _ := io.ReadAll(out)
		if err := cmd.Wait(); err != nil {
			logger.Warn("[notification] notify-send exited badly", "err", err)
		}
		if key := strings.TrimSpace(string(b)); key != "" && req.ID != "" {
			deliver(req.ID, key)
		}
	}()
	return nil
}

// Same round trip as Windows: notify-send's --action carries the app's own URL.
func clickable(req NotifyRequest, scheme string) bool { return req.ID != "" && scheme != "" }

// notify-send is spawned per call, so there is nothing to set up first.
func prepare() {}
