package service

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

var (
	ErrRequestQueueFull            = errors.New("request queue is full")
	ErrRequestQueueUserPendingFull = errors.New("request queue user pending limit exceeded")
)

type RequestQueueLease struct{}

func (RequestQueueLease) Release() {}

type requestQueueWaiter struct {
	enqueuedAt             time.Time
	userID                 int
	username               string
	channelName            string
	tokenName              string
	tokenGroup             string
	userPendingReserved    bool
	userPendingReleaseOnce sync.Once
	ready                  chan struct{}
	removed                bool
}

type RequestQueueSnapshotItem struct {
	ChannelID      int    `json:"channel_id"`
	ChannelName    string `json:"channel_name"`
	UserID         int    `json:"user_id"`
	Username       string `json:"username"`
	TokenName      string `json:"token_name"`
	TokenGroup     string `json:"token_group"`
	EnqueuedAt     int64  `json:"enqueued_at"`
	WaitingSeconds int64  `json:"waiting_seconds"`
	QueuePosition  int    `json:"queue_position"`
}

type RequestQueueChannelSnapshotItem struct {
	ChannelID          int    `json:"channel_id"`
	ChannelName        string `json:"channel_name"`
	QueuedRequestCount int    `json:"queued_request_count"`
	QueuedUserCount    int    `json:"queued_user_count"`
	CurrentRPM         int    `json:"current_rpm"`
}

type RequestQueueSnapshot struct {
	Items    []RequestQueueSnapshotItem        `json:"items"`
	Channels []RequestQueueChannelSnapshotItem `json:"channels"`
}

type requestQueueLimiter struct {
	rpm    int
	nextAt time.Time
}

type requestQueueUserPendingCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func (l *requestQueueLimiter) readyAt(now time.Time) time.Time {
	if l == nil || l.rpm <= 0 {
		return now
	}
	if l.nextAt.IsZero() || !l.nextAt.After(now) {
		return now
	}
	return l.nextAt
}

func (l *requestQueueLimiter) reserve(at time.Time) {
	if l == nil || l.rpm <= 0 {
		return
	}
	interval := time.Minute / time.Duration(l.rpm)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	if l.nextAt.After(at) {
		l.nextAt = l.nextAt.Add(interval)
		return
	}
	l.nextAt = at.Add(interval)
}

type channelRequestQueue struct {
	channelID      int
	channelName    string
	channelLimiter requestQueueLimiter
	waiters        []*requestQueueWaiter
	dispatchTimes  []time.Time
	wake           chan struct{}
	mu             sync.Mutex
}

type requestQueueScheduler interface {
	choose(q *channelRequestQueue) *requestQueueWaiter
	afterDispatch(q *channelRequestQueue, waiter *requestQueueWaiter)
}

type requestQueueSchedulerFactory func() requestQueueScheduler

type requestQueueSchedulerKey struct {
	channelID int
	queue     *channelRequestQueue
	strategy  string
}

var requestQueueSchedulerFactories = map[string]requestQueueSchedulerFactory{
	setting.RequestQueueScheduleStrategyFIFO:     func() requestQueueScheduler { return fifoRequestQueueScheduler{} },
	setting.RequestQueueScheduleStrategyUserLoop: func() requestQueueScheduler { return &userLoopRequestQueueScheduler{} },
}

var (
	requestQueueSchedulers      sync.Map
	requestQueueSchedulerStates sync.Map
	requestQueueUserPending     = &requestQueueUserPendingCounter{counts: map[string]int{}}
)

func init() {
	setting.RegisterRequestQueueConfigChangeHook(signalAllRequestQueueSchedulers)
}

func (c *requestQueueUserPendingCounter) tryReserve(username string, maxPending int) bool {
	if c == nil || username == "" || maxPending <= 0 {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts == nil {
		c.counts = map[string]int{}
	}
	if c.counts[username] >= maxPending {
		return false
	}
	c.counts[username]++
	return true
}

func (c *requestQueueUserPendingCounter) release(username string) {
	if c == nil || username == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts == nil || c.counts[username] <= 0 {
		return
	}
	c.counts[username]--
	if c.counts[username] == 0 {
		delete(c.counts, username)
	}
}

func (c *requestQueueUserPendingCounter) get(username string) int {
	if c == nil || username == "" {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[username]
}

func tryReserveRequestQueueUserPending(username string, maxPending int) bool {
	return requestQueueUserPending.tryReserve(username, maxPending)
}

func releaseRequestQueueUserPending(username string) {
	requestQueueUserPending.release(username)
}

func (w *requestQueueWaiter) releaseUserPending() {
	if w == nil || !w.userPendingReserved || w.username == "" {
		return
	}
	w.userPendingReleaseOnce.Do(func() {
		releaseRequestQueueUserPending(w.username)
	})
}

func (w *requestQueueWaiter) userLoopKey() string {
	if w == nil {
		return "anonymous"
	}
	if w.userID > 0 {
		return "user_id:" + strconv.Itoa(w.userID)
	}
	username := strings.TrimSpace(w.username)
	if username != "" {
		return "username:" + username
	}
	return "anonymous"
}

func getChannelRequestQueue(channelID int) *channelRequestQueue {
	actual, _ := requestQueueSchedulers.LoadOrStore(channelID, newChannelRequestQueue(channelID))
	return actual.(*channelRequestQueue)
}

func signalAllRequestQueueSchedulers() {
	requestQueueSchedulers.Range(func(_, value any) bool {
		queue, ok := value.(*channelRequestQueue)
		if ok {
			queue.signal()
		}
		return true
	})
}

func newChannelRequestQueue(channelID int) *channelRequestQueue {
	q := &channelRequestQueue{
		channelID: channelID,
		wake:      make(chan struct{}, 1),
	}
	go q.loop()
	return q
}

func (q *channelRequestQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *channelRequestQueue) syncConfig(channelName string, channelRPM int) {
	q.mu.Lock()
	q.syncConfigLocked(channelName, channelRPM)
	q.mu.Unlock()
}

func (q *channelRequestQueue) syncConfigLocked(channelName string, channelRPM int) {
	if strings.TrimSpace(channelName) != "" {
		q.channelName = strings.TrimSpace(channelName)
	}
	if q.channelLimiter.rpm == channelRPM {
		return
	}
	q.channelLimiter.rpm = channelRPM
	q.channelLimiter.nextAt = time.Time{}
}

func (q *channelRequestQueue) enqueue(waiter *requestQueueWaiter) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if setting.RequestQueueMaxChannelPending > 0 && len(q.waiters) >= setting.RequestQueueMaxChannelPending {
		return ErrRequestQueueFull
	}
	q.waiters = append(q.waiters, waiter)
	if waiter.channelName != "" {
		q.channelName = waiter.channelName
	}
	q.signal()
	return nil
}

func (q *channelRequestQueue) remove(waiter *requestQueueWaiter) {
	q.mu.Lock()
	defer q.mu.Unlock()
	waiter.removed = true
	for i, item := range q.waiters {
		if item == waiter {
			q.waiters = append(q.waiters[:i], q.waiters[i+1:]...)
			break
		}
	}
	waiter.releaseUserPending()
	q.signal()
}

func (q *channelRequestQueue) pendingCount() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	count := 0
	for _, waiter := range q.waiters {
		if waiter != nil && !waiter.removed {
			count++
		}
	}
	return count
}

func (q *channelRequestQueue) snapshot(now time.Time) []RequestQueueSnapshotItem {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	items := make([]RequestQueueSnapshotItem, 0, len(q.waiters))
	queuePosition := 0
	for _, waiter := range q.waiters {
		if waiter == nil || waiter.removed {
			continue
		}
		queuePosition++
		waitingSeconds := int64(now.Sub(waiter.enqueuedAt).Seconds())
		if waitingSeconds < 0 {
			waitingSeconds = 0
		}
		items = append(items, RequestQueueSnapshotItem{
			ChannelID:      q.channelID,
			ChannelName:    waiter.channelName,
			UserID:         waiter.userID,
			Username:       waiter.username,
			TokenName:      waiter.tokenName,
			TokenGroup:     waiter.tokenGroup,
			EnqueuedAt:     waiter.enqueuedAt.Unix(),
			WaitingSeconds: waitingSeconds,
			QueuePosition:  queuePosition,
		})
	}
	return items
}

func (q *channelRequestQueue) channelSnapshot(now time.Time) RequestQueueChannelSnapshotItem {
	if q == nil {
		return RequestQueueChannelSnapshotItem{}
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	q.pruneDispatchTimesLocked(now)
	queuedUsers := make(map[string]struct{})
	queuedRequestCount := 0
	channelName := q.currentChannelNameLocked()
	for _, waiter := range q.waiters {
		if waiter == nil || waiter.removed {
			continue
		}
		queuedRequestCount++
		queuedUsers[waiter.userLoopKey()] = struct{}{}
		if channelName == "" && strings.TrimSpace(waiter.channelName) != "" {
			channelName = strings.TrimSpace(waiter.channelName)
		}
	}

	return RequestQueueChannelSnapshotItem{
		ChannelID:          q.channelID,
		ChannelName:        channelName,
		QueuedRequestCount: queuedRequestCount,
		QueuedUserCount:    len(queuedUsers),
		CurrentRPM:         len(q.dispatchTimes),
	}
}

func (q *channelRequestQueue) pruneDispatchTimesLocked(now time.Time) {
	cutoff := now.Add(-time.Minute)
	keepFrom := 0
	for keepFrom < len(q.dispatchTimes) && q.dispatchTimes[keepFrom].Before(cutoff) {
		keepFrom++
	}
	if keepFrom > 0 {
		q.dispatchTimes = append(q.dispatchTimes[:0], q.dispatchTimes[keepFrom:]...)
	}
}

func (q *channelRequestQueue) recordDispatchLocked(now time.Time) {
	q.pruneDispatchTimesLocked(now)
	q.dispatchTimes = append(q.dispatchTimes, now)
}

func (q *channelRequestQueue) currentChannelNameLocked() string {
	if strings.TrimSpace(q.channelName) != "" {
		return strings.TrimSpace(q.channelName)
	}
	for _, waiter := range q.waiters {
		if waiter == nil || waiter.removed {
			continue
		}
		if strings.TrimSpace(waiter.channelName) != "" {
			return strings.TrimSpace(waiter.channelName)
		}
	}
	return ""
}

func resolveRequestQueueChannelRPMByName(channelName string) int {
	if !setting.RequestQueueEnabled {
		return 0
	}
	if rpm, found := setting.GetRequestQueueChannelRPM(strings.TrimSpace(channelName)); found {
		return rpm
	}
	return setting.RequestQueueDefaultChannelRPM
}

func (q *channelRequestQueue) refreshConfigLocked() {
	channelName := q.currentChannelNameLocked()
	q.syncConfigLocked(channelName, resolveRequestQueueChannelRPMByName(channelName))
}

func GetRequestQueuePendingCounts(channelIDs []int) map[int]int {
	counts := make(map[int]int, len(channelIDs))
	if len(channelIDs) == 0 {
		return counts
	}

	wanted := make(map[int]struct{}, len(channelIDs))
	for _, channelID := range channelIDs {
		if channelID <= 0 {
			continue
		}
		wanted[channelID] = struct{}{}
		counts[channelID] = 0
	}
	if len(wanted) == 0 {
		return counts
	}

	requestQueueSchedulers.Range(func(key, value any) bool {
		channelID, ok := key.(int)
		if !ok {
			return true
		}
		if _, found := wanted[channelID]; !found {
			return true
		}
		queue, ok := value.(*channelRequestQueue)
		if !ok {
			return true
		}
		counts[channelID] = queue.pendingCount()
		return true
	})
	return counts
}

func GetRequestQueueSnapshot() RequestQueueSnapshot {
	now := time.Now()
	items := make([]RequestQueueSnapshotItem, 0)
	channels := make([]RequestQueueChannelSnapshotItem, 0)
	requestQueueSchedulers.Range(func(_, value any) bool {
		queue, ok := value.(*channelRequestQueue)
		if !ok {
			return true
		}
		items = append(items, queue.snapshot(now)...)
		channels = append(channels, queue.channelSnapshot(now))
		return true
	})

	sort.Slice(items, func(i, j int) bool {
		if items[i].EnqueuedAt != items[j].EnqueuedAt {
			return items[i].EnqueuedAt < items[j].EnqueuedAt
		}
		if items[i].ChannelID != items[j].ChannelID {
			return items[i].ChannelID < items[j].ChannelID
		}
		return items[i].QueuePosition < items[j].QueuePosition
	})
	sort.Slice(channels, func(i, j int) bool {
		if channels[i].QueuedRequestCount != channels[j].QueuedRequestCount {
			return channels[i].QueuedRequestCount > channels[j].QueuedRequestCount
		}
		if channels[i].ChannelID != channels[j].ChannelID {
			return channels[i].ChannelID < channels[j].ChannelID
		}
		return channels[i].ChannelName < channels[j].ChannelName
	})
	return RequestQueueSnapshot{
		Items:    items,
		Channels: channels,
	}
}

func (q *channelRequestQueue) chooseLocked(now time.Time) (*requestQueueWaiter, time.Time, requestQueueScheduler) {
	q.refreshConfigLocked()
	scheduler := q.currentSchedulerLocked()
	selected := scheduler.choose(q)
	if selected == nil {
		return nil, time.Time{}, scheduler
	}
	channelReadyAt := q.channelLimiter.readyAt(now)
	if channelReadyAt.After(now) {
		return nil, channelReadyAt, scheduler
	}
	return selected, time.Time{}, scheduler
}

func (q *channelRequestQueue) currentSchedulerLocked() requestQueueScheduler {
	strategy := setting.NormalizeRequestQueueScheduleStrategy(setting.RequestQueueScheduleStrategy)
	factory, ok := requestQueueSchedulerFactories[strategy]
	if !ok {
		factory = requestQueueSchedulerFactories[setting.RequestQueueScheduleStrategyFIFO]
	}
	key := requestQueueSchedulerKey{
		channelID: q.channelID,
		strategy:  strategy,
	}
	if q.channelID <= 0 {
		key.queue = q
	}
	if actual, ok := requestQueueSchedulerStates.Load(key); ok {
		return actual.(requestQueueScheduler)
	}
	actual, _ := requestQueueSchedulerStates.LoadOrStore(key, factory())
	return actual.(requestQueueScheduler)
}

type fifoRequestQueueScheduler struct{}

func (fifoRequestQueueScheduler) choose(q *channelRequestQueue) *requestQueueWaiter {
	var selected *requestQueueWaiter
	for _, waiter := range q.waiters {
		if waiter == nil || waiter.removed {
			continue
		}
		if selected == nil || waiter.enqueuedAt.Before(selected.enqueuedAt) {
			selected = waiter
		}
	}
	return selected
}

func (fifoRequestQueueScheduler) afterDispatch(_ *channelRequestQueue, _ *requestQueueWaiter) {}

type userLoopRequestQueueScheduler struct {
	userLoop     []string
	userLoopNext int
}

func (s *userLoopRequestQueueScheduler) choose(q *channelRequestQueue) *requestQueueWaiter {
	s.sync(q)
	for len(s.userLoop) > 0 {
		if s.userLoopNext < 0 || s.userLoopNext >= len(s.userLoop) {
			s.userLoopNext = 0
		}
		userKey := s.userLoop[s.userLoopNext]
		if waiter := firstRequestQueueWaiterForUser(q.waiters, userKey); waiter != nil {
			return waiter
		}
		s.removeAt(s.userLoopNext)
	}
	return nil
}

func firstRequestQueueWaiterForUser(waiters []*requestQueueWaiter, userKey string) *requestQueueWaiter {
	var selected *requestQueueWaiter
	for _, waiter := range waiters {
		if waiter == nil || waiter.removed || waiter.userLoopKey() != userKey {
			continue
		}
		if selected == nil || waiter.enqueuedAt.Before(selected.enqueuedAt) {
			selected = waiter
		}
	}
	return selected
}

func (s *userLoopRequestQueueScheduler) sync(q *channelRequestQueue) {
	pending := make(map[string]struct{}, len(q.waiters))
	for _, waiter := range q.waiters {
		if waiter == nil || waiter.removed {
			continue
		}
		pending[waiter.userLoopKey()] = struct{}{}
	}
	if len(pending) == 0 {
		s.userLoop = nil
		s.userLoopNext = 0
		return
	}

	nextKey := ""
	nextIndex := s.userLoopNext
	if len(s.userLoop) > 0 {
		if nextIndex < 0 || nextIndex >= len(s.userLoop) {
			nextIndex = 0
		}
		nextKey = s.userLoop[nextIndex]
	}

	seen := make(map[string]struct{}, len(pending))
	synced := make([]string, 0, len(pending))
	for _, userKey := range s.userLoop {
		if _, ok := pending[userKey]; !ok {
			continue
		}
		if _, ok := seen[userKey]; ok {
			continue
		}
		seen[userKey] = struct{}{}
		synced = append(synced, userKey)
	}
	for _, waiter := range q.waiters {
		if waiter == nil || waiter.removed {
			continue
		}
		userKey := waiter.userLoopKey()
		if _, ok := seen[userKey]; ok {
			continue
		}
		seen[userKey] = struct{}{}
		synced = append(synced, userKey)
	}

	s.userLoop = synced
	if len(s.userLoop) == 0 {
		s.userLoopNext = 0
		return
	}
	if nextKey != "" {
		for index, userKey := range s.userLoop {
			if userKey == nextKey {
				s.userLoopNext = index
				return
			}
		}
	}
	if nextIndex >= len(s.userLoop) {
		s.userLoopNext = 0
		return
	}
	s.userLoopNext = nextIndex
}

func (s *userLoopRequestQueueScheduler) hasPendingUser(q *channelRequestQueue, userKey string) bool {
	return firstRequestQueueWaiterForUser(q.waiters, userKey) != nil
}

func (s *userLoopRequestQueueScheduler) indexOf(userKey string) int {
	for index, existing := range s.userLoop {
		if existing == userKey {
			return index
		}
	}
	return -1
}

func (s *userLoopRequestQueueScheduler) removeAt(index int) {
	if index < 0 || index >= len(s.userLoop) {
		s.userLoopNext = 0
		return
	}
	s.userLoop = append(s.userLoop[:index], s.userLoop[index+1:]...)
	if len(s.userLoop) == 0 {
		s.userLoopNext = 0
		return
	}
	if index >= len(s.userLoop) {
		s.userLoopNext = 0
		return
	}
	s.userLoopNext = index
}

func (s *userLoopRequestQueueScheduler) afterDispatch(q *channelRequestQueue, waiter *requestQueueWaiter) {
	s.sync(q)
	if len(s.userLoop) == 0 || waiter == nil {
		return
	}

	userKey := waiter.userLoopKey()
	index := s.indexOf(userKey)
	if index < 0 {
		return
	}
	if s.hasPendingUser(q, userKey) {
		s.userLoopNext = (index + 1) % len(s.userLoop)
		return
	}
	s.removeAt(index)
}

func (q *channelRequestQueue) dispatch(waiter *requestQueueWaiter, now time.Time, scheduler requestQueueScheduler) bool {
	for i, item := range q.waiters {
		if item == waiter {
			q.waiters = append(q.waiters[:i], q.waiters[i+1:]...)
			break
		}
	}
	q.channelLimiter.reserve(now)
	q.recordDispatchLocked(now)
	if scheduler != nil {
		scheduler.afterDispatch(q, waiter)
	}
	waiter.releaseUserPending()
	close(waiter.ready)
	return true
}

func (q *channelRequestQueue) loop() {
	for {
		q.mu.Lock()
		now := time.Now()
		waiter, earliest, scheduler := q.chooseLocked(now)
		if waiter != nil {
			dispatched := q.dispatch(waiter, now, scheduler)
			q.mu.Unlock()
			if dispatched {
				continue
			}
			continue
		} else {
			q.mu.Unlock()
		}

		if earliest.IsZero() {
			<-q.wake
			continue
		}

		timer := time.NewTimer(time.Until(earliest))
		select {
		case <-q.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}
}

func resolveRequestQueueChannelRPM(c *gin.Context, info *relaycommon.RelayInfo) int {
	if info == nil || info.ChannelMeta == nil {
		return 0
	}
	channelName := common.GetContextKeyString(c, constant.ContextKeyChannelName)
	return resolveRequestQueueChannelRPMByName(channelName)
}

func resolveRequestQueueUserMaxPending(c *gin.Context, info *relaycommon.RelayInfo) (string, int) {
	username := strings.TrimSpace(common.GetContextKeyString(c, constant.ContextKeyUserName))
	maxPending := setting.RequestQueueDefaultUserMaxPending
	if username != "" {
		if override, found := setting.GetRequestQueueUserMaxPending(username); found {
			maxPending = override
		}
		return username, maxPending
	}
	if info != nil && info.UserId > 0 {
		return "user_id:" + strconv.Itoa(info.UserId), maxPending
	}
	return "", maxPending
}

func RequestQueueAcquire(c *gin.Context, info *relaycommon.RelayInfo) (*RequestQueueLease, *types.NewAPIError) {
	if !setting.RequestQueueEnabled || c == nil || info == nil || info.ChannelMeta == nil {
		return &RequestQueueLease{}, nil
	}
	if info.RelayMode == relayconstant.RelayModeRealtime {
		return &RequestQueueLease{}, nil
	}

	channelRPM := resolveRequestQueueChannelRPM(c, info)
	if channelRPM <= 0 {
		return &RequestQueueLease{}, nil
	}

	queue := getChannelRequestQueue(info.ChannelId)
	channelName := strings.TrimSpace(common.GetContextKeyString(c, constant.ContextKeyChannelName))
	queue.syncConfig(channelName, channelRPM)

	username, userMaxPending := resolveRequestQueueUserMaxPending(c, info)
	userPendingReserved := false
	if userMaxPending > 0 {
		if !tryReserveRequestQueueUserPending(username, userMaxPending) {
			return nil, types.NewErrorWithStatusCode(
				ErrRequestQueueUserPendingFull,
				types.ErrorCodeRequestQueueFailed,
				http.StatusTooManyRequests,
				types.ErrOptionWithSkipRetry(),
			)
		}
		userPendingReserved = username != ""
	}

	waiter := &requestQueueWaiter{
		enqueuedAt:          time.Now(),
		userID:              info.UserId,
		username:            username,
		channelName:         channelName,
		tokenName:           strings.TrimSpace(c.GetString("token_name")),
		tokenGroup:          strings.TrimSpace(common.GetContextKeyString(c, constant.ContextKeyTokenGroup)),
		userPendingReserved: userPendingReserved,
		ready:               make(chan struct{}),
	}
	if err := queue.enqueue(waiter); err != nil {
		waiter.releaseUserPending()
		return nil, types.NewErrorWithStatusCode(
			err,
			types.ErrorCodeRequestQueueFailed,
			http.StatusTooManyRequests,
			types.ErrOptionWithSkipRetry(),
		)
	}

	waitCtx := c.Request.Context()
	select {
	case <-waiter.ready:
		return &RequestQueueLease{}, nil
	case <-waitCtx.Done():
		queue.remove(waiter)
		return nil, types.NewErrorWithStatusCode(
			waitCtx.Err(),
			types.ErrorCodeRequestQueueFailed,
			http.StatusRequestTimeout,
			types.ErrOptionWithSkipRetry(),
		)
	}
}
