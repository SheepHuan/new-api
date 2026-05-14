package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

func GetRequestQueueLogs(c *gin.Context) {
	common.ApiSuccess(c, service.GetRequestQueueSnapshot())
}
