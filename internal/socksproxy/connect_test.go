package socksproxy

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A late handler entry after cleanup, or failure to join an admitted handler,
// must fail this focused proof without a scheduler-dependent HTTP interleaving.
func TestConnectHandlerLifetime(t *testing.T) {
	t.Run("late entry rejected", func(t *testing.T) {
		var calls atomic.Int32
		gate := newHandlerLifetime(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
		gate.closeAdmission()
		gate.wait()
		gate.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodConnect, "http://fixture.invalid", nil))
		if calls.Load() != 0 {
			t.Fatal("supplied handler invoked after cleanup completed")
		}
	})
	t.Run("admitted handler joined", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		returned := make(chan struct{})
		gate := newHandlerLifetime(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { close(entered); <-release }))
		go func() {
			defer close(returned)
			gate.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodConnect, "http://fixture.invalid", nil))
		}()
		<-entered
		gate.closeAdmission()
		joined := make(chan struct{})
		go func() { gate.wait(); close(joined) }()
		select {
		case <-joined:
			close(release)
			<-returned
			t.Fatal("cleanup returned before admitted handler finished")
		case <-time.After(40 * time.Millisecond):
		}
		close(release)
		select {
		case <-joined:
		case <-time.After(time.Second):
			t.Fatal("cleanup did not join completed handler")
		}
		<-returned
	})
}
