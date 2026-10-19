package imchannel_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"velagateway/internal/imchannel"
)

func TestRetry_StopsOnAPermanentError(t *testing.T) {
	rejected := errors.New("rejected")
	calls := 0
	err := imchannel.Retry(context.Background(), "test", func(e error) bool { return errors.Is(e, rejected) }, func() error {
		calls++
		return rejected
	})
	if !errors.Is(err, rejected) || calls != 1 {
		t.Fatalf("err = %v after %d calls, want the permanent error after one call", err, calls)
	}
}

func TestRetry_RetriesATransientErrorUntilItSucceeds(t *testing.T) {
	calls := 0
	err := imchannel.Retry(context.Background(), "test", func(error) bool { return false }, func() error {
		calls++
		if calls == 1 {
			return errors.New("flaky")
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("err = %v after %d calls, want success on the second", err, calls)
	}
}

func TestRetry_ReturnsNilWhenCancelledWhileBackingOff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := imchannel.Retry(ctx, "test", func(error) bool { return false }, func() error { return errors.New("down") })
	if err != nil || time.Since(start) > 900*time.Millisecond {
		t.Fatalf("err = %v after %v, want nil soon after the cancel", err, time.Since(start))
	}
}

func TestNoRedirects_HandsBackTheRedirectAndLeavesTheOriginalAlone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			t.Error("the redirect must not be followed")
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	orig := &http.Client{Transport: &http.Transport{}}
	c := imchannel.NoRedirects(orig)
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want the 302 itself", resp.StatusCode)
	}
	if orig.CheckRedirect != nil {
		t.Error("the original client must stay untouched")
	}
	if c.Transport != orig.Transport {
		t.Error("the copy must keep the guarded transport")
	}
	if imchannel.NoRedirects(nil) != nil {
		t.Error("nil in, nil out")
	}
}

func TestGuardedDial_RefusesAClientWithoutAControlledDialer(t *testing.T) {
	for name, c := range map[string]*http.Client{
		"nil client":                    nil,
		"default transport":             {},
		"transport without DialContext": {Transport: &http.Transport{}},
	} {
		if _, err := imchannel.GuardedDial(c); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
	dial, err := imchannel.GuardedDial(&http.Client{Transport: &http.Transport{DialContext: (&net.Dialer{}).DialContext}})
	if err != nil || dial == nil {
		t.Fatalf("a transport with its own DialContext must be accepted: %v", err)
	}
}

func TestRecent_RemembersTheLast512IDs(t *testing.T) {
	var r imchannel.Recent
	if !r.First("m-0") || r.First("m-0") {
		t.Fatal("the second sighting of an id is a duplicate")
	}
	if !r.First("") || !r.First("") {
		t.Fatal("empty ids are never deduplicated")
	}
	for i := 1; i <= 512; i++ {
		r.First(fmt.Sprintf("m-%d", i))
	}
	if !r.First("m-0") {
		t.Fatal("the oldest id is forgotten once 512 newer ones arrived")
	}
	if r.First("m-512") {
		t.Fatal("a recent id is still remembered")
	}
}
