package setting

import "testing"

func withRequestQueueConfigHookForTest(t *testing.T, hook func()) {
	t.Helper()
	requestQueueConfigHookMutex.Lock()
	originalHooks := requestQueueConfigChangeHooks
	requestQueueConfigChangeHooks = nil
	if hook != nil {
		requestQueueConfigChangeHooks = append(requestQueueConfigChangeHooks, hook)
	}
	requestQueueConfigHookMutex.Unlock()

	t.Cleanup(func() {
		requestQueueConfigHookMutex.Lock()
		requestQueueConfigChangeHooks = originalHooks
		requestQueueConfigHookMutex.Unlock()
	})
}

func TestRequestQueueChannelRPM2JSONStringReturnsObjectForEmptyMap(t *testing.T) {
	RequestQueueMutex.Lock()
	original := RequestQueueChannelRPM
	RequestQueueChannelRPM = map[string]int{}
	RequestQueueMutex.Unlock()
	t.Cleanup(func() {
		RequestQueueMutex.Lock()
		RequestQueueChannelRPM = original
		RequestQueueMutex.Unlock()
	})

	if got := RequestQueueChannelRPM2JSONString(); got != "{}" {
		t.Fatalf("expected empty channel RPM JSON to render as empty object, got %q", got)
	}
}

func TestRequestQueueDefaultUserMaxPendingDefault(t *testing.T) {
	if RequestQueueDefaultUserMaxPending != 32 {
		t.Fatalf("expected default user max pending to be 32, got %d", RequestQueueDefaultUserMaxPending)
	}
}

func TestRequestQueueConfigSettersNotify(t *testing.T) {
	notifications := 0
	withRequestQueueConfigHookForTest(t, func() {
		notifications++
	})
	originalEnabled := RequestQueueEnabled
	originalDefaultChannelRPM := RequestQueueDefaultChannelRPM
	originalMaxChannelPending := RequestQueueMaxChannelPending
	originalDefaultUserMaxPending := RequestQueueDefaultUserMaxPending
	originalScheduleStrategy := RequestQueueScheduleStrategy
	originalChannelRPM := RequestQueueChannelRPM
	originalUserMaxPending := RequestQueueUserMaxPending
	t.Cleanup(func() {
		RequestQueueEnabled = originalEnabled
		RequestQueueDefaultChannelRPM = originalDefaultChannelRPM
		RequestQueueMaxChannelPending = originalMaxChannelPending
		RequestQueueDefaultUserMaxPending = originalDefaultUserMaxPending
		RequestQueueScheduleStrategy = originalScheduleStrategy
		RequestQueueMutex.Lock()
		RequestQueueChannelRPM = originalChannelRPM
		RequestQueueUserMaxPending = originalUserMaxPending
		RequestQueueMutex.Unlock()
	})

	SetRequestQueueEnabled(true)
	SetRequestQueueDefaultChannelRPM(3)
	SetRequestQueueMaxChannelPending(512)
	SetRequestQueueDefaultUserMaxPending(32)
	SetRequestQueueScheduleStrategy(RequestQueueScheduleStrategyUserLoop)
	if err := UpdateRequestQueueChannelRPMByJSONString(`{"hot-channel":3}`); err != nil {
		t.Fatalf("expected channel RPM update to succeed, got %v", err)
	}
	if err := UpdateRequestQueueUserMaxPendingByJSONString(`{"test1":32}`); err != nil {
		t.Fatalf("expected user max pending update to succeed, got %v", err)
	}

	if notifications != 7 {
		t.Fatalf("expected 7 queue config notifications, got %d", notifications)
	}
}
