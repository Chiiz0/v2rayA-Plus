package controller

import (
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/v2rayA/v2rayA/common"
	"github.com/v2rayA/v2rayA/db/configure"
	"github.com/v2rayA/v2rayA/pkg/util/log"
	"github.com/v2rayA/v2rayA/server/service"
)

func GetDockerProxies(ctx *gin.Context) {
	proxies, err := service.GetDockerProxies()
	if err != nil {
		log.Error("GetDockerProxies error: %v", err)
		common.ResponseError(ctx, err)
		return
	}
	common.ResponseSuccess(ctx, proxies)
}

type postDockerProxyReq struct {
	Which      configure.Which           `json:"which"`
	FrontWhich *configure.Which          `json:"frontWhich,omitempty"`
	Port       int                       `json:"port"`
	Core       configure.DockerProxyCore `json:"core"`
}

func PostDockerProxy(ctx *gin.Context) {
	var req postDockerProxyReq
	err := ctx.ShouldBindJSON(&req)
	if err != nil {
		log.Error("PostDockerProxy bind error: %v", err)
		common.ResponseError(ctx, fmt.Errorf("bad request: %w", err))
		return
	}
	err = service.CreateDockerProxy(req.Which, req.FrontWhich, req.Port, req.Core)
	if err != nil {
		log.Error("CreateDockerProxy error: %v", err)
		common.ResponseError(ctx, err)
		return
	}
	common.ResponseSuccess(ctx, nil)
}

func PutDockerProxy(ctx *gin.Context) {
	var req postDockerProxyReq
	err := ctx.ShouldBindJSON(&req)
	if err != nil {
		log.Error("PutDockerProxy bind error: %v", err)
		common.ResponseError(ctx, fmt.Errorf("bad request: %w", err))
		return
	}
	err = service.UpdateDockerProxy(req.Port, req.Which, req.FrontWhich, req.Core)
	if err != nil {
		log.Error("UpdateDockerProxy error: %v", err)
		common.ResponseError(ctx, err)
		return
	}
	common.ResponseSuccess(ctx, nil)
}

type deleteDockerProxyReq struct {
	Port int `json:"port"`
}

func DeleteDockerProxy(ctx *gin.Context) {
	var req deleteDockerProxyReq
	err := ctx.ShouldBindJSON(&req)
	if err != nil {
		log.Error("DeleteDockerProxy bind error: %v", err)
		common.ResponseError(ctx, fmt.Errorf("bad request: %w", err))
		return
	}
	err = service.DeleteDockerProxy(req.Port)
	if err != nil {
		log.Error("DeleteDockerProxy error: %v", err)
		common.ResponseError(ctx, err)
		return
	}
	common.ResponseSuccess(ctx, nil)
}

func PauseDockerProxy(ctx *gin.Context) {
	var req deleteDockerProxyReq
	err := ctx.ShouldBindJSON(&req)
	if err != nil {
		log.Error("PauseDockerProxy bind error: %v", err)
		common.ResponseError(ctx, fmt.Errorf("bad request: %w", err))
		return
	}
	err = service.PauseDockerProxy(req.Port)
	if err != nil {
		log.Error("PauseDockerProxy error: %v", err)
		common.ResponseError(ctx, err)
		return
	}
	common.ResponseSuccess(ctx, nil)
}

func ResumeDockerProxy(ctx *gin.Context) {
	var req deleteDockerProxyReq
	err := ctx.ShouldBindJSON(&req)
	if err != nil {
		log.Error("ResumeDockerProxy bind error: %v", err)
		common.ResponseError(ctx, fmt.Errorf("bad request: %w", err))
		return
	}
	err = service.ResumeDockerProxy(req.Port)
	if err != nil {
		log.Error("ResumeDockerProxy error: %v", err)
		common.ResponseError(ctx, err)
		return
	}
	common.ResponseSuccess(ctx, nil)
}
