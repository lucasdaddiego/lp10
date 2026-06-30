package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// secOutcome is the result of invoking the OS secret-store lookup: a non-zero rc,
// a timeout, or an inability to run the tool at all (the OSError class).
type secOutcome struct {
	stdout, stderr string
	rc             int
	timeout        bool
	runErr         error // could not execute the lookup tool
}

// runSecurity invokes the OS secret-store lookup (secretLookupArgv); overridable
// in tests.
var runSecurity = realRunSecurity

func realRunSecurity() secOutcome {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	argv := secretLookupArgv()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	o := secOutcome{stdout: out.String(), stderr: errb.String()}
	if err != nil {
		// Only a timeout if the deadline actually interrupted the process: a
		// success landing right at the deadline (err == nil) must return the
		// password, matching Python's TimeoutExpired-only semantics.
		if ctx.Err() == context.DeadlineExceeded {
			o.timeout = true
			return o
		}
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			o.rc = exitErr.ExitCode()
		} else { // the lookup tool is missing/not executable
			o.runErr = err
		}
	}
	return o
}

// KeychainPassword reads the LP10 password from the OS secret store — the macOS
// login Keychain via security(1), or the Secret Service via secret-tool on Linux
// (see secret_darwin.go / secret_linux.go).
func KeychainPassword() (string, error) {
	o := runSecurity()
	if o.timeout {
		return "", &TransportError{MarkerLocked, true, 60 * time.Second}
	}
	if o.runErr != nil {
		return "", &TransportError{fmt.Sprintf("%s: %v", MarkerBroken, o.runErr), true, 60 * time.Second}
	}
	if o.rc != 0 {
		if secretNotFound(o) {
			return "", &TransportError{MarkerNoItem, true, 10 * time.Second}
		}
		return "", &TransportError{MarkerLocked, true, 60 * time.Second}
	}
	pw := strings.TrimSuffix(o.stdout, "\n")
	if pw == "" {
		// A clean exit with no output means the item is absent (secret-tool may
		// exit 0 in that case) — treat it as no-item, not an empty password.
		return "", &TransportError{MarkerNoItem, true, 10 * time.Second}
	}
	return pw, nil
}

// AskpassMain answers ssh's password prompt from the Keychain. Failure markers
// go to stderr (shared with the parent's ssh stderr pipe); it exits the process.
func AskpassMain() {
	pw, err := KeychainPassword()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	fmt.Println(pw)
	os.Exit(0)
}
