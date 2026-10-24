package controller

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/v2rayA/v2rayA/common"
	"github.com/v2rayA/v2rayA/core/v2ray/asset"
	"github.com/v2rayA/v2rayA/server/service"
)

func GetSetting(ctx *gin.Context) {
	s := service.GetSetting()
	var localGFWListVersion string
	t, err := asset.GetGFWListModTime()
	if err == nil {
		localGFWListVersion = t.Local().Format("2006-01-02")
	}
	common.ResponseSuccess(ctx, gin.H{
		"setting":             s,
		"localGFWListVersion": localGFWListVersion,
	})
}

func PutSetting(ctx *gin.Context) {
	updatingMu.Lock()
	if updating {
		common.ResponseError(ctx, processingErr)
		updatingMu.Unlock()
		return
	}
	updating = true
	updatingMu.Unlock()
	defer func() {
		updatingMu.Lock()
		updating = false
		updatingMu.Unlock()
	}()

	var data json.RawMessage
	err := ctx.ShouldBindJSON(&data)
	if err != nil {
		common.ResponseError(ctx, logError("bad request"))
		return
	}
	err = service.PatchSetting(data)
	if err != nil {
		common.ResponseError(ctx, logError(err))
		return
	}
	common.ResponseSuccess(ctx, nil)
}
