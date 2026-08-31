package singleinstance

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func testID(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("scorix-test-%d-%s", os.Getpid(), sanitize(t.Name()))
}

func TestSecondAcquireActivatesPrimary(t *testing.T) {
	got := make(chan struct{}, 1)
	l, err := Acquire(testID(t), func([]string) {
		select {
		case got <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatalf("primary Acquire: %v", err)
	}
	defer l.Release()

	if _, err := Acquire(testID(t), nil); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Acquire err = %v, want ErrAlreadyRunning", err)
	}
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("primary's onActivate never fired")
	}
}

func TestReleaseThenReacquire(t *testing.T) {
	l, err := Acquire(testID(t), nil)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	l.Release()
	l.Release() // idempotent

	l2, err := Acquire(testID(t), nil)
	if err != nil {
		t.Fatalf("re-Acquire after Release: %v", err)
	}
	l2.Release()
}

func TestCrossProcess(t *testing.T) {
	if os.Getenv("SI_HELPER") == "1" {
		if _, err := Acquire(os.Getenv("SI_ID"), nil); errors.Is(err, ErrAlreadyRunning) {
			fmt.Println("SECONDARY")
			os.Exit(0)
		}
		fmt.Println("PRIMARY-UNEXPECTED")
		os.Exit(2)
	}

	id := testID(t)
	got := make(chan []string, 1)
	l, err := Acquire(id, func(args []string) {
		select {
		case got <- args:
		default:
		}
	})
	if err != nil {
		t.Fatalf("primary Acquire: %v", err)
	}
	defer l.Release()

	cmd := exec.Command(os.Args[0], "-test.run", "^TestCrossProcess$", "-test.v")
	cmd.Env = append(os.Environ(), "SI_HELPER=1", "SI_ID="+id)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "SECONDARY") {
		t.Fatalf("helper err=%v out=%s", err, out)
	}
	select {
	case args := <-got:
		// The secondary forwards its argv; the helper's carries the -test.run flag.
		if !strings.Contains(strings.Join(args, " "), "-test.run") {
			t.Fatalf("forwarded args = %v, want the helper's own argv", args)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cross-process activation never reached the primary")
	}
}

func TestSanitize(t *testing.T) {
	if got := sanitize("com.tradalab/x y"); got != "com.tradalab_x_y" {
		t.Fatalf("sanitize = %q", got)
	}
	if got := sanitize(""); got != "scorix-app" {
		t.Fatalf("sanitize empty = %q", got)
	}
}
