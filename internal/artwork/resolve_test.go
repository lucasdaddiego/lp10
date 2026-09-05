package artwork

import (
	"context"
	"errors"
	"net"
	"testing"
)

// A cover host that fails to resolve is given up on only when the name does
// not exist; a resolver timeout (or a lookup cut short by the fetch deadline)
// is transient, so the art worker retries it. The device-exemption lookup is
// judged the same way.
func TestDialVettedResolveFailuresAreClassified(t *testing.T) {
	orig := lookupIP
	t.Cleanup(func() { lookupIP = orig })
	timeout := &net.DNSError{Err: "i/o timeout", Name: "cover.example", IsTimeout: true}
	missing := &net.DNSError{Err: "no such host", Name: "cover.example", IsNotFound: true}
	ctx := context.Background()

	lookupIP = func(context.Context, string, string) ([]net.IP, error) { return nil, timeout }
	if _, err := dialVetted(ctx, "tcp", "cover.example:80"); err == nil || errors.Is(err, ErrUndecodable) {
		t.Errorf("resolver timeout = %v, want a transient error", err)
	}
	lookupIP = func(context.Context, string, string) ([]net.IP, error) { return nil, missing }
	if _, err := dialVetted(ctx, "tcp", "cover.example:80"); !errors.Is(err, ErrUndecodable) {
		t.Errorf("nonexistent host = %v, want ErrUndecodable", err)
	}

	// The cover resolves to a blocked address; whether the device exemption
	// covers it can't be judged while the device's own lookup is failing.
	allowCtx := context.WithValue(ctx, allowHostKey, "device.local")
	lookupIP = func(_ context.Context, _, host string) ([]net.IP, error) {
		if host == "device.local" {
			return nil, timeout
		}
		return []net.IP{net.IPv4(192, 168, 0, 13)}, nil
	}
	if _, err := dialVetted(allowCtx, "tcp", "cover.example:80"); err == nil || errors.Is(err, ErrUndecodable) {
		t.Errorf("exemption lookup timeout = %v, want a transient error", err)
	}
	// ...and a device name that does not exist leaves the block standing.
	lookupIP = func(_ context.Context, _, host string) ([]net.IP, error) {
		if host == "device.local" {
			return nil, missing
		}
		return []net.IP{net.IPv4(192, 168, 0, 13)}, nil
	}
	if _, err := dialVetted(allowCtx, "tcp", "cover.example:80"); !errors.Is(err, ErrUndecodable) {
		t.Errorf("blocked candidate with a nonexistent exemption = %v, want ErrUndecodable", err)
	}
}
