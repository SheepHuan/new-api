package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/require"
)

func resetRateLimitSchedulerOptionsForTest(t *testing.T) {
	t.Helper()
	require.NoError(t, DB.AutoMigrate(&Option{}))
	require.NoError(t, DB.Delete(&Option{}, []string{"ModelRequestRateLimitEnabled", "RequestQueueEnabled"}).Error)

	common.OptionMapRWMutex.Lock()
	originalOptionMap := common.OptionMap
	common.OptionMap = map[string]string{
		"ModelRequestRateLimitEnabled": "false",
		"RequestQueueEnabled":          "false",
	}
	common.OptionMapRWMutex.Unlock()

	originalModelRateLimitEnabled := setting.ModelRequestRateLimitEnabled
	originalRequestQueueEnabled := setting.RequestQueueEnabled

	t.Cleanup(func() {
		require.NoError(t, DB.Delete(&Option{}, []string{"ModelRequestRateLimitEnabled", "RequestQueueEnabled"}).Error)
		common.OptionMapRWMutex.Lock()
		common.OptionMap = originalOptionMap
		common.OptionMapRWMutex.Unlock()
		setting.ModelRequestRateLimitEnabled = originalModelRateLimitEnabled
		setting.RequestQueueEnabled = originalRequestQueueEnabled
	})
}

func getOptionValueForTest(t *testing.T, key string) string {
	t.Helper()
	var option Option
	require.NoError(t, DB.Where(&Option{Key: key}).First(&option).Error)
	return option.Value
}

func TestUpdateOptionKeepsRateLimitAndRequestSchedulerMutuallyExclusive(t *testing.T) {
	resetRateLimitSchedulerOptionsForTest(t)

	require.NoError(t, UpdateOption("RequestQueueEnabled", "true"))
	require.True(t, setting.RequestQueueEnabled)
	require.False(t, setting.ModelRequestRateLimitEnabled)
	require.Equal(t, "true", getOptionValueForTest(t, "RequestQueueEnabled"))
	require.Equal(t, "false", getOptionValueForTest(t, "ModelRequestRateLimitEnabled"))

	require.NoError(t, UpdateOption("ModelRequestRateLimitEnabled", "true"))
	require.True(t, setting.ModelRequestRateLimitEnabled)
	require.False(t, setting.RequestQueueEnabled)
	require.Equal(t, "true", getOptionValueForTest(t, "ModelRequestRateLimitEnabled"))
	require.Equal(t, "false", getOptionValueForTest(t, "RequestQueueEnabled"))
}
