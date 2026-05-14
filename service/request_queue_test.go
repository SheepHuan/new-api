package service

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
)

func resetRequestQueueSchedulersForTest() {
	requestQueueSchedulers.Range(func(key, _ any) bool {
		requestQueueSchedulers.Delete(key)
		return true
	})
	requestQueueSchedulerStates.Range(func(key, _ any) bool {
		requestQueueSchedulerStates.Delete(key)
		return true
	})
}

func resetRequestQueueUserPendingForTest() {
	requestQueueUserPending.mu.Lock()
	defer requestQueueUserPending.mu.Unlock()
	requestQueueUserPending.counts = map[string]int{}
}

func setRequestQueueStrategyForTest(t *testing.T, strategy string) {
	t.Helper()
	original := setting.RequestQueueScheduleStrategy
	setting.RequestQueueScheduleStrategy = strategy
	t.Cleanup(func() {
		setting.RequestQueueScheduleStrategy = original
	})
}

func setRequestQueueUserMaxPendingForTest(t *testing.T, defaultMax int, overrides map[string]int) {
	t.Helper()
	originalDefault := setting.RequestQueueDefaultUserMaxPending
	setting.RequestQueueMutex.Lock()
	originalOverrides := setting.RequestQueueUserMaxPending
	setting.RequestQueueDefaultUserMaxPending = defaultMax
	setting.RequestQueueUserMaxPending = overrides
	setting.RequestQueueMutex.Unlock()
	t.Cleanup(func() {
		setting.RequestQueueMutex.Lock()
		setting.RequestQueueDefaultUserMaxPending = originalDefault
		setting.RequestQueueUserMaxPending = originalOverrides
		setting.RequestQueueMutex.Unlock()
	})
}

func TestRequestQueueUsesFIFO(t *testing.T) {
	setRequestQueueStrategyForTest(t, setting.RequestQueueScheduleStrategyFIFO)
	now := time.Now()
	first := &requestQueueWaiter{enqueuedAt: now.Add(-2 * time.Second), ready: make(chan struct{})}
	second := &requestQueueWaiter{enqueuedAt: now.Add(-1 * time.Second), ready: make(chan struct{})}
	queue := &channelRequestQueue{
		waiters: []*requestQueueWaiter{first, second},
	}

	waiter, _, _ := queue.chooseLocked(now)
	if waiter == nil {
		t.Fatal("expected a waiter to be selected")
	}
	if waiter != first {
		t.Fatal("expected earlier waiter to be selected first")
	}
}

func TestRequestQueueUserLoopStrategyRoundsAcrossUsers(t *testing.T) {
	setRequestQueueStrategyForTest(t, setting.RequestQueueScheduleStrategyUserLoop)
	now := time.Now()
	userAFirst := &requestQueueWaiter{userID: 1, enqueuedAt: now.Add(-4 * time.Second), ready: make(chan struct{})}
	userASecond := &requestQueueWaiter{userID: 1, enqueuedAt: now.Add(-3 * time.Second), ready: make(chan struct{})}
	userB := &requestQueueWaiter{userID: 2, enqueuedAt: now.Add(-2 * time.Second), ready: make(chan struct{})}
	userC := &requestQueueWaiter{userID: 3, enqueuedAt: now.Add(-1 * time.Second), ready: make(chan struct{})}
	queue := &channelRequestQueue{
		waiters: []*requestQueueWaiter{userAFirst, userASecond, userB, userC},
	}

	waiter, _, scheduler := queue.chooseLocked(now)
	if waiter != userAFirst {
		t.Fatalf("expected user A first request first, got %+v", waiter)
	}
	queue.dispatch(waiter, now, scheduler)

	waiter, _, scheduler = queue.chooseLocked(now)
	if waiter != userB {
		t.Fatalf("expected user B after user A, got %+v", waiter)
	}
	queue.dispatch(waiter, now, scheduler)

	waiter, _, scheduler = queue.chooseLocked(now)
	if waiter != userC {
		t.Fatalf("expected user C after user B, got %+v", waiter)
	}
	queue.dispatch(waiter, now, scheduler)

	waiter, _, _ = queue.chooseLocked(now)
	if waiter != userASecond {
		t.Fatalf("expected user A second request after user C, got %+v", waiter)
	}
}

func TestRequestQueueUserLoopStrategyAppendsNewUserAfterExistingLoop(t *testing.T) {
	setRequestQueueStrategyForTest(t, setting.RequestQueueScheduleStrategyUserLoop)
	now := time.Now()
	userA := &requestQueueWaiter{userID: 1, enqueuedAt: now.Add(-4 * time.Second), ready: make(chan struct{})}
	userB := &requestQueueWaiter{userID: 2, enqueuedAt: now.Add(-3 * time.Second), ready: make(chan struct{})}
	userC := &requestQueueWaiter{userID: 3, enqueuedAt: now.Add(-2 * time.Second), ready: make(chan struct{})}
	userD := &requestQueueWaiter{userID: 4, enqueuedAt: now.Add(-1 * time.Second), ready: make(chan struct{})}
	queue := &channelRequestQueue{
		waiters: []*requestQueueWaiter{userA, userB, userC},
	}

	waiter, _, scheduler := queue.chooseLocked(now)
	if waiter != userA {
		t.Fatalf("expected user A first, got %+v", waiter)
	}
	queue.dispatch(waiter, now, scheduler)

	if err := queue.enqueue(userD); err != nil {
		t.Fatalf("expected enqueue to succeed, got %v", err)
	}

	waiter, _, scheduler = queue.chooseLocked(now)
	if waiter != userB {
		t.Fatalf("expected user B after user A, got %+v", waiter)
	}
	queue.dispatch(waiter, now, scheduler)

	waiter, _, scheduler = queue.chooseLocked(now)
	if waiter != userC {
		t.Fatalf("expected user C before new user D, got %+v", waiter)
	}
	queue.dispatch(waiter, now, scheduler)

	waiter, _, _ = queue.chooseLocked(now)
	if waiter != userD {
		t.Fatalf("expected new user D after existing loop, got %+v", waiter)
	}
}

func TestRequestQueueUserLoopStrategySelectsSingleQueuedUser(t *testing.T) {
	setRequestQueueStrategyForTest(t, setting.RequestQueueScheduleStrategyUserLoop)
	now := time.Now()
	first := &requestQueueWaiter{userID: 7, enqueuedAt: now.Add(-2 * time.Second), ready: make(chan struct{})}
	second := &requestQueueWaiter{userID: 7, enqueuedAt: now.Add(-1 * time.Second), ready: make(chan struct{})}
	queue := &channelRequestQueue{
		waiters: []*requestQueueWaiter{second, first},
	}

	waiter, _, _ := queue.chooseLocked(now)
	if waiter == nil {
		t.Fatal("expected a waiter to be selected")
	}
	if waiter != first {
		t.Fatal("expected earliest request from the only queued user")
	}
}

func TestRequestQueueUserLoopStrategyAppliesOnNextDispatchAfterSwitch(t *testing.T) {
	setRequestQueueStrategyForTest(t, setting.RequestQueueScheduleStrategyUserLoop)
	now := time.Now()
	userAFirst := &requestQueueWaiter{userID: 1, enqueuedAt: now.Add(-3 * time.Second), ready: make(chan struct{})}
	userASecond := &requestQueueWaiter{userID: 1, enqueuedAt: now.Add(-2 * time.Second), ready: make(chan struct{})}
	userB := &requestQueueWaiter{userID: 2, enqueuedAt: now.Add(-1 * time.Second), ready: make(chan struct{})}
	queue := &channelRequestQueue{
		waiters: []*requestQueueWaiter{userAFirst, userASecond, userB},
	}

	waiter, _, scheduler := queue.chooseLocked(now)
	if waiter != userAFirst {
		t.Fatalf("expected user loop to select user A first, got %+v", waiter)
	}
	queue.dispatch(waiter, now, scheduler)

	setting.RequestQueueScheduleStrategy = setting.RequestQueueScheduleStrategyFIFO
	waiter, _, _ = queue.chooseLocked(now)
	if waiter != userASecond {
		t.Fatalf("expected FIFO to apply on next dispatch and select user A second, got %+v", waiter)
	}

	setting.RequestQueueScheduleStrategy = setting.RequestQueueScheduleStrategyUserLoop
	waiter, _, _ = queue.chooseLocked(now)
	if waiter != userB {
		t.Fatalf("expected user loop to apply on next dispatch and select user B, got %+v", waiter)
	}
}

func TestRequestQueueWaitsForChannelRPM(t *testing.T) {
	setRequestQueueStrategyForTest(t, setting.RequestQueueScheduleStrategyFIFO)
	originalEnabled := setting.RequestQueueEnabled
	originalDefaultRPM := setting.RequestQueueDefaultChannelRPM
	setting.RequestQueueEnabled = true
	setting.RequestQueueDefaultChannelRPM = 60
	t.Cleanup(func() {
		setting.RequestQueueEnabled = originalEnabled
		setting.RequestQueueDefaultChannelRPM = originalDefaultRPM
	})

	now := time.Now()
	waiter := &requestQueueWaiter{enqueuedAt: now.Add(-2 * time.Second), ready: make(chan struct{})}
	queue := &channelRequestQueue{
		channelLimiter: requestQueueLimiter{rpm: 60, nextAt: now.Add(time.Second)},
		waiters:        []*requestQueueWaiter{waiter},
	}

	selected, earliest, _ := queue.chooseLocked(now)
	if selected != nil {
		t.Fatal("expected no waiter while channel limiter is cooling down")
	}
	if !earliest.Equal(queue.channelLimiter.nextAt) {
		t.Fatalf("expected earliest to be channel nextAt, got %v", earliest)
	}
}

func TestResolveRequestQueueChannelRPMUsesChannelNameOverride(t *testing.T) {
	originalEnabled := setting.RequestQueueEnabled
	originalDefault := setting.RequestQueueDefaultChannelRPM
	originalChannel := setting.RequestQueueChannelRPM
	defer func() {
		setting.RequestQueueEnabled = originalEnabled
		setting.RequestQueueDefaultChannelRPM = originalDefault
		setting.RequestQueueMutex.Lock()
		setting.RequestQueueChannelRPM = originalChannel
		setting.RequestQueueMutex.Unlock()
	}()

	setting.RequestQueueEnabled = true
	setting.RequestQueueDefaultChannelRPM = 5
	setting.RequestQueueMutex.Lock()
	setting.RequestQueueChannelRPM = map[string]int{"openai-main": 30}
	setting.RequestQueueMutex.Unlock()

	c := &gin.Context{}
	common.SetContextKey(c, constant.ContextKeyChannelName, "openai-main")

	rpm := resolveRequestQueueChannelRPM(c, &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 1},
	})
	if rpm != 30 {
		t.Fatalf("expected channel RPM override, got %d", rpm)
	}
}

func TestRequestQueueRefreshesChannelRPMBeforeNextDispatch(t *testing.T) {
	setRequestQueueStrategyForTest(t, setting.RequestQueueScheduleStrategyFIFO)
	originalEnabled := setting.RequestQueueEnabled
	originalDefault := setting.RequestQueueDefaultChannelRPM
	originalChannel := setting.RequestQueueChannelRPM
	t.Cleanup(func() {
		setting.RequestQueueEnabled = originalEnabled
		setting.RequestQueueDefaultChannelRPM = originalDefault
		setting.RequestQueueMutex.Lock()
		setting.RequestQueueChannelRPM = originalChannel
		setting.RequestQueueMutex.Unlock()
	})

	setting.RequestQueueEnabled = true
	setting.RequestQueueDefaultChannelRPM = 60
	setting.RequestQueueMutex.Lock()
	setting.RequestQueueChannelRPM = map[string]int{"hot-channel": 3}
	setting.RequestQueueMutex.Unlock()

	now := time.Now()
	waiter := &requestQueueWaiter{
		channelName: "hot-channel",
		enqueuedAt:  now.Add(-time.Second),
		ready:       make(chan struct{}),
	}
	queue := &channelRequestQueue{
		channelID:      1001,
		channelName:    "hot-channel",
		channelLimiter: requestQueueLimiter{rpm: 60, nextAt: now.Add(time.Hour)},
		waiters:        []*requestQueueWaiter{waiter},
	}

	selected, earliest, _ := queue.chooseLocked(now)
	if selected != waiter {
		t.Fatalf("expected queued request to be selected after RPM refresh, got %+v", selected)
	}
	if !earliest.IsZero() {
		t.Fatalf("expected dispatch to be ready after RPM refresh, got earliest %v", earliest)
	}
	if queue.channelLimiter.rpm != 3 {
		t.Fatalf("expected hot channel RPM 3 to apply before dispatch, got %d", queue.channelLimiter.rpm)
	}
}

func TestRequestQueueDisableAppliesBeforeNextDispatch(t *testing.T) {
	setRequestQueueStrategyForTest(t, setting.RequestQueueScheduleStrategyFIFO)
	originalEnabled := setting.RequestQueueEnabled
	originalDefault := setting.RequestQueueDefaultChannelRPM
	t.Cleanup(func() {
		setting.RequestQueueEnabled = originalEnabled
		setting.RequestQueueDefaultChannelRPM = originalDefault
	})

	setting.RequestQueueEnabled = false
	setting.RequestQueueDefaultChannelRPM = 60
	now := time.Now()
	waiter := &requestQueueWaiter{
		enqueuedAt: now.Add(-time.Second),
		ready:      make(chan struct{}),
	}
	queue := &channelRequestQueue{
		channelID:      1004,
		channelLimiter: requestQueueLimiter{rpm: 60, nextAt: now.Add(time.Hour)},
		waiters:        []*requestQueueWaiter{waiter},
	}

	selected, earliest, _ := queue.chooseLocked(now)
	if selected != waiter {
		t.Fatalf("expected disabled request queue to release next waiter, got %+v", selected)
	}
	if !earliest.IsZero() {
		t.Fatalf("expected disabled request queue to dispatch immediately, got earliest %v", earliest)
	}
	if queue.channelLimiter.rpm != 0 {
		t.Fatalf("expected disabled request queue to clear channel limiter, got %d", queue.channelLimiter.rpm)
	}
}

func TestRequestQueueConfigChangeSignalsExistingQueues(t *testing.T) {
	resetRequestQueueSchedulersForTest()
	t.Cleanup(resetRequestQueueSchedulersForTest)

	originalChannel := setting.RequestQueueChannelRPM
	t.Cleanup(func() {
		setting.RequestQueueMutex.Lock()
		setting.RequestQueueChannelRPM = originalChannel
		setting.RequestQueueMutex.Unlock()
	})

	queue := &channelRequestQueue{
		channelID: 1002,
		wake:      make(chan struct{}, 1),
	}
	requestQueueSchedulers.Store(queue.channelID, queue)

	if err := setting.UpdateRequestQueueChannelRPMByJSONString(`{"hot-channel":3}`); err != nil {
		t.Fatalf("expected channel RPM update to succeed, got %v", err)
	}

	select {
	case <-queue.wake:
	case <-time.After(time.Second):
		t.Fatal("expected request queue config update to wake existing queue")
	}
}

func TestRequestQueueChannelSnapshotCountsRecentDispatchRPM(t *testing.T) {
	now := time.Now()
	queue := &channelRequestQueue{
		channelID:   1003,
		channelName: "rpm-channel",
		dispatchTimes: []time.Time{
			now.Add(-70 * time.Second),
			now.Add(-30 * time.Second),
			now.Add(-5 * time.Second),
		},
		waiters: []*requestQueueWaiter{
			{userID: 1, channelName: "rpm-channel", ready: make(chan struct{})},
			{userID: 1, channelName: "rpm-channel", ready: make(chan struct{})},
			{userID: 2, channelName: "rpm-channel", ready: make(chan struct{})},
		},
	}

	summary := queue.channelSnapshot(now)
	if summary.CurrentRPM != 2 {
		t.Fatalf("expected current RPM 2 from recent dispatches, got %d", summary.CurrentRPM)
	}
	if summary.QueuedRequestCount != 3 {
		t.Fatalf("expected 3 queued requests, got %d", summary.QueuedRequestCount)
	}
	if summary.QueuedUserCount != 2 {
		t.Fatalf("expected 2 queued users, got %d", summary.QueuedUserCount)
	}
}

func TestResolveRequestQueueUserMaxPendingUsesUsernameOverride(t *testing.T) {
	setRequestQueueUserMaxPendingForTest(t, 1, map[string]int{
		"test1": 2,
		"test2": 0,
	})

	c := &gin.Context{}
	common.SetContextKey(c, constant.ContextKeyUserName, "test1")

	username, maxPending := resolveRequestQueueUserMaxPending(c, &relaycommon.RelayInfo{UserId: 1})
	if username != "test1" {
		t.Fatalf("expected username test1, got %q", username)
	}
	if maxPending != 2 {
		t.Fatalf("expected user override max pending 2, got %d", maxPending)
	}

	c = &gin.Context{}
	common.SetContextKey(c, constant.ContextKeyUserName, "test2")
	_, maxPending = resolveRequestQueueUserMaxPending(c, &relaycommon.RelayInfo{UserId: 2})
	if maxPending != 0 {
		t.Fatalf("expected explicit user override 0 to mean unlimited, got %d", maxPending)
	}
}

func TestResolveRequestQueueUserMaxPendingFallsBackToDefault(t *testing.T) {
	setRequestQueueUserMaxPendingForTest(t, 3, map[string]int{
		"test1": 2,
	})

	c := &gin.Context{}
	common.SetContextKey(c, constant.ContextKeyUserName, "test3")

	username, maxPending := resolveRequestQueueUserMaxPending(c, &relaycommon.RelayInfo{UserId: 3})
	if username != "test3" {
		t.Fatalf("expected username test3, got %q", username)
	}
	if maxPending != 3 {
		t.Fatalf("expected default max pending 3, got %d", maxPending)
	}
}

func TestRequestQueueUserPendingLimitIsSharedAcrossQueues(t *testing.T) {
	resetRequestQueueUserPendingForTest()
	t.Cleanup(resetRequestQueueUserPendingForTest)

	if !tryReserveRequestQueueUserPending("test1", 1) {
		t.Fatal("expected first pending request to reserve")
	}
	if tryReserveRequestQueueUserPending("test1", 1) {
		t.Fatal("expected second pending request from same user to be rejected")
	}
	if !tryReserveRequestQueueUserPending("test2", 1) {
		t.Fatal("expected another user to reserve independently")
	}

	releaseRequestQueueUserPending("test1")
	if !tryReserveRequestQueueUserPending("test1", 1) {
		t.Fatal("expected reservation after release to succeed")
	}
}

func TestRequestQueueWaiterReleasesUserPendingOnce(t *testing.T) {
	resetRequestQueueUserPendingForTest()
	t.Cleanup(resetRequestQueueUserPendingForTest)

	if !tryReserveRequestQueueUserPending("test1", 2) {
		t.Fatal("expected reservation to succeed")
	}
	waiter := &requestQueueWaiter{
		username:            "test1",
		userPendingReserved: true,
	}

	waiter.releaseUserPending()
	waiter.releaseUserPending()

	if count := requestQueueUserPending.get("test1"); count != 0 {
		t.Fatalf("expected pending count to be released once, got %d", count)
	}
}

func TestRequestQueueAcquireRejectsWhenUserPendingLimitReached(t *testing.T) {
	resetRequestQueueSchedulersForTest()
	resetRequestQueueUserPendingForTest()
	t.Cleanup(resetRequestQueueSchedulersForTest)
	t.Cleanup(resetRequestQueueUserPendingForTest)

	originalEnabled := setting.RequestQueueEnabled
	originalDefaultChannelRPM := setting.RequestQueueDefaultChannelRPM
	originalStrategy := setting.RequestQueueScheduleStrategy
	defer func() {
		setting.RequestQueueEnabled = originalEnabled
		setting.RequestQueueDefaultChannelRPM = originalDefaultChannelRPM
		setting.RequestQueueScheduleStrategy = originalStrategy
	}()
	setRequestQueueUserMaxPendingForTest(t, 1, nil)

	setting.RequestQueueEnabled = true
	setting.RequestQueueDefaultChannelRPM = 1
	setting.RequestQueueScheduleStrategy = setting.RequestQueueScheduleStrategyFIFO
	if !tryReserveRequestQueueUserPending("test1", 1) {
		t.Fatal("expected seed reservation to succeed")
	}

	c := &gin.Context{}
	common.SetContextKey(c, constant.ContextKeyUserName, "test1")
	_, apiErr := RequestQueueAcquire(c, &relaycommon.RelayInfo{
		UserId: 1,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: 101,
		},
	})
	if apiErr == nil {
		t.Fatal("expected user pending limit error")
	}
	if !errors.Is(apiErr, ErrRequestQueueUserPendingFull) {
		t.Fatalf("expected ErrRequestQueueUserPendingFull, got %v", apiErr)
	}
	if apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %d", apiErr.StatusCode)
	}
}

func TestRequestQueueRejectsOnlyWhenChannelQueueIsFull(t *testing.T) {
	originalMaxChannelPending := setting.RequestQueueMaxChannelPending
	defer func() {
		setting.RequestQueueMaxChannelPending = originalMaxChannelPending
	}()

	setting.RequestQueueMaxChannelPending = 2
	queue := &channelRequestQueue{
		waiters: []*requestQueueWaiter{
			{ready: make(chan struct{})},
			{ready: make(chan struct{})},
		},
	}

	if err := queue.enqueue(&requestQueueWaiter{ready: make(chan struct{})}); err != ErrRequestQueueFull {
		t.Fatalf("expected full channel queue error, got %v", err)
	}
}

func TestRequestQueueAllowsMoreRequestsUntilChannelQueueIsFull(t *testing.T) {
	originalMaxChannelPending := setting.RequestQueueMaxChannelPending
	defer func() {
		setting.RequestQueueMaxChannelPending = originalMaxChannelPending
	}()

	setting.RequestQueueMaxChannelPending = 3
	queue := &channelRequestQueue{
		waiters: []*requestQueueWaiter{
			{ready: make(chan struct{})},
		},
	}

	if err := queue.enqueue(&requestQueueWaiter{ready: make(chan struct{})}); err != nil {
		t.Fatalf("expected request to queue, got %v", err)
	}
}

func TestGetRequestQueuePendingCounts(t *testing.T) {
	resetRequestQueueSchedulersForTest()
	t.Cleanup(resetRequestQueueSchedulersForTest)

	requestQueueSchedulers.Store(11, &channelRequestQueue{
		channelID: 11,
		waiters: []*requestQueueWaiter{
			{ready: make(chan struct{})},
			{ready: make(chan struct{}), removed: true},
			{ready: make(chan struct{})},
		},
	})
	requestQueueSchedulers.Store(12, &channelRequestQueue{
		channelID: 12,
		waiters: []*requestQueueWaiter{
			{ready: make(chan struct{})},
		},
	})

	counts := GetRequestQueuePendingCounts([]int{11, 12, 13})
	if counts[11] != 2 {
		t.Fatalf("expected channel 11 to have 2 pending requests, got %d", counts[11])
	}
	if counts[12] != 1 {
		t.Fatalf("expected channel 12 to have 1 pending request, got %d", counts[12])
	}
	if counts[13] != 0 {
		t.Fatalf("expected channel 13 to default to 0 pending requests, got %d", counts[13])
	}
}

func TestGetRequestQueueSnapshot(t *testing.T) {
	resetRequestQueueSchedulersForTest()
	t.Cleanup(resetRequestQueueSchedulersForTest)

	now := time.Now()
	requestQueueSchedulers.Store(21, &channelRequestQueue{
		channelID: 21,
		waiters: []*requestQueueWaiter{
			{
				enqueuedAt:  now.Add(-1 * time.Second),
				userID:      2,
				username:    "test2",
				channelName: "channel-b",
				tokenName:   "token-b",
				tokenGroup:  "default",
				ready:       make(chan struct{}),
			},
			{
				enqueuedAt:  now.Add(-3 * time.Second),
				userID:      1,
				username:    "test1",
				channelName: "channel-b",
				tokenName:   "token-a",
				tokenGroup:  "default",
				ready:       make(chan struct{}),
			},
		},
	})
	requestQueueSchedulers.Store(22, &channelRequestQueue{
		channelID: 22,
		waiters: []*requestQueueWaiter{
			{
				enqueuedAt:  now.Add(-2 * time.Second),
				userID:      3,
				username:    "test3",
				channelName: "channel-c",
				ready:       make(chan struct{}),
			},
			{
				enqueuedAt:  now.Add(-4 * time.Second),
				userID:      4,
				username:    "removed",
				channelName: "channel-c",
				ready:       make(chan struct{}),
				removed:     true,
			},
		},
	})

	snapshot := GetRequestQueueSnapshot()
	items := snapshot.Items
	if len(items) != 3 {
		t.Fatalf("expected 3 queued items, got %d", len(items))
	}
	if len(snapshot.Channels) != 2 {
		t.Fatalf("expected 2 channel summary rows, got %d", len(snapshot.Channels))
	}
	if snapshot.Channels[0].ChannelID != 21 || snapshot.Channels[0].QueuedRequestCount != 2 || snapshot.Channels[0].QueuedUserCount != 2 {
		t.Fatalf("unexpected first channel summary: %+v", snapshot.Channels[0])
	}
	if items[0].Username != "test1" || items[0].ChannelID != 21 || items[0].QueuePosition != 2 {
		t.Fatalf("unexpected first queued item: %+v", items[0])
	}
	if items[0].TokenName != "token-a" || items[0].TokenGroup != "default" {
		t.Fatalf("unexpected token fields on first queued item: %+v", items[0])
	}
	if items[1].Username != "test3" || items[1].ChannelName != "channel-c" {
		t.Fatalf("unexpected second queued item: %+v", items[1])
	}
	if items[2].Username != "test2" || items[2].WaitingSeconds < 0 {
		t.Fatalf("unexpected third queued item: %+v", items[2])
	}
}
