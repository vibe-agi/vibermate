//go:build darwin

package desktoptrust

import "testing"

func TestProductionCommandExecutorClassifiesAuthorizationMessages(t *testing.T) {
	if !commandUserCancelled([]byte("The user canceled the operation.")) {
		t.Fatal("user cancellation was not recognized")
	}
	if !commandPermissionDenied([]byte("Authorization was denied.")) {
		t.Fatal("permission denial was not recognized")
	}
	if !commandPermissionDenied([]byte("The user name or passphrase you entered is not correct.")) {
		t.Fatal("failed administrator authentication was not recognized")
	}
}
