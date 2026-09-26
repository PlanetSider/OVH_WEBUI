package handlers

import "testing"

func TestAcquireInstallLockReclaimsIdleEntries(t *testing.T) {
	lease, ok := acquireInstallLock("svc-lock-test")
	if !ok || lease == nil {
		t.Fatal("first install lock acquisition failed")
	}
	if _, ok := acquireInstallLock("svc-lock-test"); ok {
		t.Fatal("second install lock acquisition unexpectedly succeeded")
	}
	lease.Unlock()

	installOSLocksMu.Lock()
	remaining := len(installOSLocks)
	installOSLocksMu.Unlock()
	if remaining != 0 {
		t.Fatalf("install lock entries = %d, want 0 after release", remaining)
	}

	lease, ok = acquireInstallLock("svc-lock-test")
	if !ok || lease == nil {
		t.Fatal("lock could not be reacquired after release")
	}
	lease.Unlock()
}
