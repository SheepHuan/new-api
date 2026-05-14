package setting

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
)

var RequestQueueEnabled = false
var RequestQueueDefaultChannelRPM = 0
var RequestQueueMaxChannelPending = 1000
var RequestQueueDefaultUserMaxPending = 32
var RequestQueueChannelRPM = map[string]int{}
var RequestQueueUserMaxPending = map[string]int{}
var RequestQueueScheduleStrategy = RequestQueueScheduleStrategyFIFO
var RequestQueueMutex sync.RWMutex
var requestQueueConfigChangeHooks []func()
var requestQueueConfigHookMutex sync.RWMutex

const (
	RequestQueueScheduleStrategyFIFO     = "fifo"
	RequestQueueScheduleStrategyUserLoop = "user_loop"
)

func RequestQueueChannelRPM2JSONString() string {
	RequestQueueMutex.RLock()
	defer RequestQueueMutex.RUnlock()

	if len(RequestQueueChannelRPM) == 0 {
		return "{}"
	}
	jsonBytes, err := common.Marshal(RequestQueueChannelRPM)
	if err != nil {
		common.SysLog("error marshalling request queue channel rpm: " + err.Error())
	}
	return string(jsonBytes)
}

func RequestQueueUserMaxPending2JSONString() string {
	RequestQueueMutex.RLock()
	defer RequestQueueMutex.RUnlock()

	jsonBytes, err := common.Marshal(RequestQueueUserMaxPending)
	if err != nil {
		common.SysLog("error marshalling request queue user max pending: " + err.Error())
	}
	return string(jsonBytes)
}

func UpdateRequestQueueChannelRPMByJSONString(jsonStr string) error {
	channelRPM, err := parseRequestQueueIntMapJSON("channel", "request queue rpm", jsonStr)
	if err != nil {
		return err
	}
	RequestQueueMutex.Lock()
	RequestQueueChannelRPM = channelRPM
	RequestQueueMutex.Unlock()
	NotifyRequestQueueConfigChanged()
	return nil
}

func UpdateRequestQueueUserMaxPendingByJSONString(jsonStr string) error {
	userMaxPending, err := parseRequestQueueIntMapJSON("user", "request queue max pending", jsonStr)
	if err != nil {
		return err
	}
	RequestQueueMutex.Lock()
	RequestQueueUserMaxPending = userMaxPending
	RequestQueueMutex.Unlock()
	NotifyRequestQueueConfigChanged()
	return nil
}

func SetRequestQueueEnabled(enabled bool) {
	RequestQueueEnabled = enabled
	NotifyRequestQueueConfigChanged()
}

func SetRequestQueueDefaultChannelRPM(rpm int) {
	RequestQueueDefaultChannelRPM = rpm
	NotifyRequestQueueConfigChanged()
}

func SetRequestQueueMaxChannelPending(maxPending int) {
	RequestQueueMaxChannelPending = maxPending
	NotifyRequestQueueConfigChanged()
}

func SetRequestQueueDefaultUserMaxPending(maxPending int) {
	RequestQueueDefaultUserMaxPending = maxPending
	NotifyRequestQueueConfigChanged()
}

func SetRequestQueueScheduleStrategy(strategy string) {
	RequestQueueScheduleStrategy = NormalizeRequestQueueScheduleStrategy(strategy)
	NotifyRequestQueueConfigChanged()
}

func RegisterRequestQueueConfigChangeHook(hook func()) {
	if hook == nil {
		return
	}
	requestQueueConfigHookMutex.Lock()
	defer requestQueueConfigHookMutex.Unlock()
	requestQueueConfigChangeHooks = append(requestQueueConfigChangeHooks, hook)
}

func NotifyRequestQueueConfigChanged() {
	requestQueueConfigHookMutex.RLock()
	hooks := append([]func(){}, requestQueueConfigChangeHooks...)
	requestQueueConfigHookMutex.RUnlock()

	for _, hook := range hooks {
		hook()
	}
}

func GetRequestQueueChannelRPM(channelName string) (int, bool) {
	RequestQueueMutex.RLock()
	defer RequestQueueMutex.RUnlock()

	if RequestQueueChannelRPM == nil {
		return 0, false
	}
	rpm, found := RequestQueueChannelRPM[channelName]
	return rpm, found
}

func GetRequestQueueUserMaxPending(username string) (int, bool) {
	RequestQueueMutex.RLock()
	defer RequestQueueMutex.RUnlock()

	if RequestQueueUserMaxPending == nil {
		return 0, false
	}
	maxPending, found := RequestQueueUserMaxPending[strings.TrimSpace(username)]
	return maxPending, found
}

func normalizeRequestQueueIntMapJSON(jsonStr string) string {
	if strings.TrimSpace(jsonStr) == "" {
		return "{}"
	}
	return jsonStr
}

func parseRequestQueueIntMapJSON(label string, field string, jsonStr string) (map[string]int, error) {
	jsonStr = normalizeRequestQueueIntMapJSON(jsonStr)
	valueMap := make(map[string]int)
	if err := common.UnmarshalJsonStr(jsonStr, &valueMap); err != nil {
		return nil, err
	}
	normalized := make(map[string]int, len(valueMap))
	for name, value := range valueMap {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("%s name cannot be empty", label)
		}
		if value < 0 {
			return nil, fmt.Errorf("%s %s has negative %s: %d", label, name, field, value)
		}
		if value > math.MaxInt32 {
			return nil, fmt.Errorf("%s %s %s %d exceeds max value 2147483647", label, name, field, value)
		}
		normalized[name] = value
	}
	return normalized, nil
}

func CheckRequestQueueIntegerOption(name string, value string) error {
	intValue, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("%s must be an integer", name)
	}
	if intValue < 0 {
		return fmt.Errorf("%s must be greater than or equal to 0", name)
	}
	if intValue > math.MaxInt32 {
		return fmt.Errorf("%s exceeds max value 2147483647", name)
	}
	return nil
}

func CheckRequestQueueChannelRPM(jsonStr string) error {
	_, err := parseRequestQueueIntMapJSON("channel", "request queue rpm", jsonStr)
	return err
}

func CheckRequestQueueUserMaxPending(jsonStr string) error {
	_, err := parseRequestQueueIntMapJSON("user", "request queue max pending", jsonStr)
	return err
}

func NormalizeRequestQueueScheduleStrategy(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	switch value {
	case RequestQueueScheduleStrategyUserLoop:
		return RequestQueueScheduleStrategyUserLoop
	default:
		return RequestQueueScheduleStrategyFIFO
	}
}

func CheckRequestQueueScheduleStrategy(value string) error {
	normalized := strings.TrimSpace(strings.ToLower(value))
	switch normalized {
	case RequestQueueScheduleStrategyFIFO, RequestQueueScheduleStrategyUserLoop:
		return nil
	default:
		return fmt.Errorf("RequestQueueScheduleStrategy must be %s or %s", RequestQueueScheduleStrategyFIFO, RequestQueueScheduleStrategyUserLoop)
	}
}
