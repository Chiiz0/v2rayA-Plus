package router

import (
	"crypto/md5"
	"embed"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/v2rayA/v2rayA/common"
	"github.com/v2rayA/v2rayA/conf"
	"github.com/v2rayA/v2rayA/db/configure"
	"github.com/v2rayA/v2rayA/pkg/server/jwt"
	"github.com/v2rayA/v2rayA/pkg/server/reqCache"
	"github.com/v2rayA/v2rayA/pkg/util/log"
	"github.com/v2rayA/v2rayA/server/controller"
	"github.com/vearutop/statigz"
)

//go:embed web
var webRoot embed.FS

// relativeFS implements fs.FS
type relativeFS struct {
	root        fs.FS
	relativeDir string
}

func (c relativeFS) Open(name string) (fs.File, error) {
	return c.root.Open(filepath.Join(c.relativeDir, name))
}

func cachedHTML(html []byte) func(ctx *gin.Context) {
	etag := fmt.Sprintf("W/%x", md5.Sum(html))
	h := string(html)
	return func(ctx *gin.Context) {
		if ctx.IsAborted() {
			return
		}
		ctx.Header("Content-Type", "text/html; charset=utf-8")
		ctx.Header("Cache-Control", "public, must-revalidate")
		ctx.Header("ETag", etag)
		if match := ctx.GetHeader("If-None-Match"); match != "" {
			if strings.Contains(match, etag) {
				ctx.Status(http.StatusNotModified)
				return
			}
		}
		ctx.String(http.StatusOK, h)
	}
}

func ServeGUI(r *gin.Engine) {
	var webFS fs.FS
	webDir := conf.GetEnvironmentConfig().WebDir
	if webDir == "" {
		webFS, _ = fs.Sub(webRoot, "web")
	} else {
		webFS = os.DirFS(webDir)
	}
	if err := serveWebFiles(r, webFS); err != nil {
		log.Warn("web UI cannot be served: %v", err)
	}

	app := conf.GetEnvironmentConfig()

	ip, port, _ := net.SplitHostPort(app.Address)
	addrs, err := net.InterfaceAddrs()
	if net.ParseIP(ip).IsUnspecified() && err == nil {
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok {
				printRunningAt("http://" + net.JoinHostPort(ipnet.IP.String(), port))
			}
		}
	} else {
		printRunningAt("http://" + app.Address)
	}
}

// serveWebFiles supports the dual build and externally supplied legacy assets.
// statigz serves both plain Vite files and the classic build's gzip-only files.
func serveWebFiles(r *gin.Engine, webFS fs.FS) error {
	html, err := fs.ReadFile(webFS, "index.html")
	if err != nil {
		return err
	}
	dirFS, ok := webFS.(fs.ReadDirFS)
	if !ok {
		return fmt.Errorf("web filesystem does not support directory reads")
	}
	var assets http.Handler = statigz.FileServer(dirFS)
	if _, err := fs.Stat(webFS, "static"); err != nil {
		assets = http.StripPrefix("/static", assets)
	}
	r.GET("/static/*w", gin.WrapH(assets))
	r.GET("/", cachedHTML(html))
	for _, name := range []string{"favicon.ico", "robots.txt"} {
		r.GET("/"+name, gin.WrapH(statigz.FileServer(dirFS)))
	}
	legacy, err := fs.Sub(webFS, "legacy")
	if err == nil {
		if html, e := fs.ReadFile(legacy, "index.html"); e == nil {
			r.GET("/legacy/", cachedHTML(html))
			r.GET("/legacy", func(c *gin.Context) { c.Header("Location", "legacy/"); c.Status(http.StatusTemporaryRedirect) })
			r.GET("/legacy/static/*w", gin.WrapH(http.StripPrefix("/legacy/static", statigz.FileServer(legacy.(fs.ReadDirFS)))))
		}
	}
	return nil
}

func nocache(c *gin.Context) {
	c.Header("Cache-Control", "no-cache, no-store, max-age=0, must-revalidate")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
}

func Run() error {
	engine := gin.New()
	//ginpprof.Wrap(engine)
	engine.Use(gin.Recovery())
	corsConfig := cors.DefaultConfig()
	corsConfig.AllowAllOrigins = true
	corsConfig.AllowMethods = []string{
		"GET", "POST", "DELETE", "PUT", "PATCH", "OPTIONS", "HEAD",
	}
	corsConfig.AllowWebSockets = true
	corsConfig.AllowCredentials = true
	corsConfig.AddAllowHeaders("Authorization", common.RequestIdHeader)
	engine.Use(cors.New(corsConfig))
	noAuth := engine.Group("api",
		nocache,
		reqCache.ReqCache,
	)
	{
		noAuth.GET("version", controller.GetVersion)
		noAuth.POST("login", controller.PostLogin)
		noAuth.POST("account", controller.PostAccount)
		noAuth.GET("account", controller.GetAccount)
	}
	auth := engine.Group("api",
		nocache,
		func(ctx *gin.Context) {
			if !configure.HasAnyAccounts() {
				common.Response(ctx, common.UNAUTHORIZED, gin.H{
					"first": true,
				})
				ctx.Abort()
				return
			}
		},
		jwt.JWTAuth(false),
		reqCache.ReqCache,
	)
	{
		auth.POST("import", controller.PostImport)
		auth.GET("touch", controller.GetTouch)
		auth.DELETE("touch", controller.DeleteTouch)
		auth.POST("connection", controller.PostConnection)
		auth.PUT("outboundConnections", controller.PutOutboundConnections)
		auth.DELETE("connection", controller.DeleteConnection)
		auth.POST("v2ray", controller.PostV2ray)
		auth.DELETE("v2ray", controller.DeleteV2ray)
		auth.GET("pingLatency", controller.GetPingLatency)
		auth.GET("httpLatency", controller.GetHttpLatency)
		auth.GET("sharingAddress", controller.GetSharingAddress)
		auth.GET("remoteGFWListVersion", controller.GetRemoteGFWListVersion)
		auth.GET("setting", controller.GetSetting)
		auth.PUT("setting", controller.PutSetting)
		auth.PUT("gfwList", controller.PutGFWList)
		auth.DELETE("gfwList", controller.DeleteGFWList)
		auth.PUT("subscription", controller.PutSubscription)
		auth.PATCH("subscription", controller.PatchSubscription)
		auth.GET("ports", controller.GetPorts)
		auth.PUT("ports", controller.PutPorts)
		//auth.PUT("account", controller.PutAccount)
		auth.GET("dnsList", controller.GetDnsList)
		auth.PUT("dnsList", controller.PutDnsList)
		auth.GET("routingA", controller.GetRoutingA)
		auth.PUT("routingA", controller.PutRoutingA)
		auth.GET("outbounds", controller.GetOutbounds)
		auth.GET("outbound", controller.GetOutbound)
		auth.POST("outbound", controller.PostOutbound)
		auth.PUT("outbound", controller.PutOutbound)
		auth.DELETE("outbound", controller.DeleteOutbound)
		auth.GET("message", controller.WsMessage)
		auth.GET("logger", controller.GetLogger)
		auth.GET("domainsExcluded", controller.GetDomainsExcluded)
		auth.GET("tproxyWhiteIpGroups", controller.GetTproxyWhiteIpGroups)
		auth.PUT("domainsExcluded", controller.PutDomainsExcluded)
		auth.PUT("tproxyWhiteIpGroups", controller.PutTproxyWhiteIpGroups)
		auth.GET("dockerProxies", controller.GetDockerProxies)
		auth.POST("dockerProxy", controller.PostDockerProxy)
		auth.GET("dockerProxy/httpLatency", controller.GetDockerProxyHttpLatency)
		auth.PUT("dockerProxy", controller.PutDockerProxy)
		auth.PUT("dockerProxy/pause", controller.PauseDockerProxy)
		auth.PUT("dockerProxy/resume", controller.ResumeDockerProxy)
		auth.DELETE("dockerProxy", controller.DeleteDockerProxy)
	}

	ServeGUI(engine)

	return engine.Run(conf.GetEnvironmentConfig().Address)
}

func printRunningAt(address string) {
	log.Alert("v2rayA is listening at %v", address)
}
