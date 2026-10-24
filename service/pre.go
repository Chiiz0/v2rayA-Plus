package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	jsonIteratorExtra "github.com/json-iterator/go/extra"
	"github.com/v2rayA/v2rayA/common/netTools/ports"
	"github.com/v2rayA/v2rayA/conf"
	"github.com/v2rayA/v2rayA/core/ipforward"
	"github.com/v2rayA/v2rayA/core/v2ray"
	"github.com/v2rayA/v2rayA/core/v2ray/asset"
	"github.com/v2rayA/v2rayA/core/v2ray/asset/dat"
	service2 "github.com/v2rayA/v2rayA/core/v2ray/service"
	"github.com/v2rayA/v2rayA/core/v2ray/where"
	"github.com/v2rayA/v2rayA/db"
	"github.com/v2rayA/v2rayA/db/configure"
	"github.com/v2rayA/v2rayA/pkg/util/log"
	"github.com/v2rayA/v2rayA/server/router"
	"github.com/v2rayA/v2rayA/server/service"

	"go.etcd.io/bbolt"
)

func checkEnvironment() (done bool, err error) {
	config := conf.GetEnvironmentConfig()
	if len(config.PrintReport) > 0 {
		config.Report()
		return true, nil
	}
	if !config.PassCheckRoot {
		if os.Getegid() != 0 {
			return false, fmt.Errorf("Please execute this program with sudo or as a root user for the best experience.\n" +
				"If you are sure you are root user, use the --passcheckroot parameter to skip the check.\n" +
				"If you don't want to run as root or you are a non-linux user, use --lite please.\n" +
				"For example:\n" +
				"$ v2raya --lite",
			)
		}
	}
	if config.ResetPassword {
		fmt.Println("Config directory:", config.Config)
		fmt.Println("Resetting password...")
		err := configure.ResetAccounts()
		if err != nil {
			return false, fmt.Errorf("reset password: %w", err)
		}
		fmt.Println("Succeed. It will work after you restart v2rayA.")
		return true, nil
	}
	_, v2rayAListeningPort, err := net.SplitHostPort(config.Address)
	if err != nil {
		return false, fmt.Errorf("listening address: %w", err)
	}
	if occupied, sockets, err := ports.IsPortOccupied([]string{v2rayAListeningPort + ":tcp"}); occupied {
		if err != nil {
			return false, fmt.Errorf("netstat: %w", err)
		}
		for _, socket := range sockets {
			process, err := socket.Process()
			if err == nil {
				return false, fmt.Errorf("port %v is occupied by %v/%v", v2rayAListeningPort, process.Name, process.PID)
			}
		}
	}
	return false, nil
}

func checkTProxySupportability() {
	if conf.GetEnvironmentConfig().Lite {
		return
	}
	//检查tproxy是否可以启用
	if err := service2.CheckAndProbeTProxy(); err != nil {
		log.Info("Cannot load TPROXY module: %v", err)
	}
}

func initDBValue() {
	log.Info("init DB")
	err := configure.SetConfigure(configure.New())
	if err != nil {
		log.Fatal("initDBValue: %v", err)
	}
}

func initConfigure(legacy *bbolt.DB) {
	//初始化配置
	jsonIteratorExtra.RegisterFuzzyDecoders()

	//db
	resolverRecovered := false
	dbPath := filepath.Join(conf.GetEnvironmentConfig().Config, "bolt.db")
	if _, e := os.Lstat(dbPath); os.IsNotExist(e) {
		previous, err := readLegacyConfiguration(legacy)
		if err != nil {
			log.Fatal("read legacy configuration: %v", err)
		}
		if previous != nil {
			// There is different format in server and subscription.
			// So we keep other content and reimport servers and subscriptions.
			log.Warn("Migrating from v4 to main")
			if err := publishLegacySnapshot(legacy, dbPath); err != nil {
				log.Fatal("Failed to copy boltv4.db to bolt.db: %v", err)
			}
			// Migration imports subscriptions below; restore DNS before those
			// network requests or their missing-resolver checks can run.
			recoverResolverOnStartup()
			resolverRecovered = true

			// clear connects of outbounds
			for _, out := range configure.GetOutbounds() {
				_ = configure.ClearConnects(out)
			}
			if err := db.Set("touch", "servers", []interface{}{}); err != nil {
				log.Fatal("prepare legacy servers: %v", err)
			}
			if err := db.Set("touch", "subscriptions", []interface{}{}); err != nil {
				log.Fatal("prepare legacy subscriptions: %v", err)
			}

			// migrate servers and subscriptions
			for i, address := range previous.subscriptions {
				log.Info("Importing legacy subscription %d", i+1)
				if e := service.Import(address, nil); e != nil {
					log.Warn("Failed to migrate legacy subscription %d", i+1)
				}
			}
			for i, address := range previous.servers {
				if e := service.Import(address, nil); e != nil {
					log.Warn("Failed to migrate legacy server %d", i+1)
				}
			}

			log.Warn("Migration is done")
		} else {
			initDBValue()
		}
	}
	if !resolverRecovered {
		recoverResolverOnStartup()
	}

	if len(configure.GetTproxyWhiteIpGroups().CountryCodes) == 0 {
		configure.SetTproxyWhiteIpGroups([]string{"PRIVATE"}, []string{})
	}

	//检查config.json是否存在
	if _, err := os.Stat(asset.GetV2rayConfigPath()); err != nil {
		//不存在就建一个。多数情况发生于docker模式挂载volume时覆盖了/etc/v2ray
		t := v2ray.Template{}
		_ = v2ray.WriteV2rayConfig(t.ToConfigBytes())
	}

	//首先确定v2ray是否存在
	if _, err := where.GetV2rayBinPath(); err == nil {
		//检查geoip、geosite是否存在
		if !asset.DoesV2rayAssetExist("geoip.dat") || !asset.DoesV2rayAssetExist("geosite.dat") {
			log.Alert("downloading missing geoip.dat and geosite.dat")
			var l net.Listener
			if l, err = net.Listen("tcp", conf.GetEnvironmentConfig().Address); err != nil {
				log.Fatal("net.Listen: %v", err)
			}
			e := gin.New()
			e.GET("/", func(c *gin.Context) {
				c.Header("Cache-Control", "no-cache, no-store, max-age=0, must-revalidate")
				c.Header("Pragma", "no-cache")
				c.Header("Expires", "0")
				c.String(200, "Downloading missing geoip.dat and geosite.dat; refresh the page later.\n正在下载缺失的 geoip.dat 和 geosite.dat，请稍后刷新页面。")
			})
			go e.RunListener(l)
			if !asset.DoesV2rayAssetExist("geoip.dat") {
				err := dat.UpdateLocalGeoIP()
				if err != nil {
					log.Fatal("UpdateLocalGeoIP: %v", err)
				}
			}
			if !asset.DoesV2rayAssetExist("geosite.dat") {
				err = dat.UpdateLocalGeoSite()
				if err != nil {
					log.Fatal("UpdateLocalGeoSite: %v", err)
				}
			}
			if l != nil {
				l.Close()
			}
			log.Alert("geoip.dat and geosite.dat are ready")
		}
	}
}

func recoverResolverOnStartup() {
	// The directory lock is already held. Also retain Bolt's exclusive lock to
	// exclude older releases that do not know the directory lock file.
	if err := db.Init(); err != nil {
		log.Fatal("initialize database before DNS recovery: %v", err)
	}
	if err := v2ray.RecoverResolvConf(); err != nil {
		log.Warn("restore DNS from the previous run: %v", err)
	}
}

func hello() {
	v2rayPath, _ := where.GetV2rayBinPath()
	log.Alert("V2Ray binary is %v", v2rayPath)
	log.Alert("V2Ray asset directory is %v", asset.GetV2rayLocationAssetOverride())
	wd, _ := os.Getwd()
	log.Alert("v2rayA working directory is %v", wd)
	log.Alert("v2rayA configuration directory is %v", conf.GetEnvironmentConfig().Config)
	log.Alert("Golang: %v", runtime.Version())
	log.Alert("OS: %v", runtime.GOOS)
	log.Alert("Arch: %v", runtime.GOARCH)
	log.Alert("Lite: %v", conf.GetEnvironmentConfig().Lite)
	log.Alert("Version: %v", conf.Version)
	log.Alert("Starting...")
}

func updateSubscriptions() {
	snapshots, err := service.SnapshotSubscriptionsForUpdate()
	if err != nil {
		log.Error("[AutoUpdate] Failed to snapshot subscriptions: %v", err)
		return
	}
	control := make(chan struct{}, 2)
	// Disconnect from subscriptions before auto-selecting servers from them
	// to limit the number of connected servers and avoid hitting the limit.
	if err := service.AutoSelectServersFromSubscriptions(true); err != nil {
		log.Error("[AutoSelect] Failed to disconnect servers from subscriptions -- err: %v", err)
	}
	wg := new(sync.WaitGroup)
	for _, snapshot := range snapshots {
		wg.Add(1)
		go func(snapshot service.SubscriptionUpdateSnapshot) {
			defer wg.Done()
			control <- struct{}{}
			defer func() { <-control }()
			// The raw snapshot is checked again at commit. A deletion, edit or
			// another refresh while this job waits must not retarget its index.
			if err := service.UpdateSubscriptionSnapshot(snapshot.Index, snapshot.Raw, false); err != nil {
				log.Info("[AutoUpdate] Subscriptions: Failed to update subscription -- ID: %d, Address: %s, err: %v", snapshot.Index, snapshot.Address, err)
			} else {
				log.Info("[AutoUpdate] Subscriptions: Complete updating subscription -- ID: %d, Address: %s", snapshot.Index, snapshot.Address)
			}
		}(snapshot)
	}
	wg.Wait()
	if err := service.AutoSelectServersFromSubscriptions(false); err != nil {
		log.Error("[AutoSelect] Failed to auto-select servers from subscriptions -- err: %v", err)
	}
}

func initUpdatingTicker() {
	conf.TickerUpdateGFWList = time.NewTicker(24 * time.Hour * 365 * 100)
	conf.TickerUpdateSubscription = time.NewTicker(24 * time.Hour * 365 * 100)
	go func() {
		for range conf.TickerUpdateGFWList.C {
			_, err := dat.CheckAndUpdateGFWList("")
			if err != nil {
				log.Info("[AutoUpdate] GFWList: %v", err)
			}
		}
	}()
	go func() {
		for range conf.TickerUpdateSubscription.C {
			updateSubscriptions()
		}
	}()
}

func checkUpdate() {
	setting := service.GetSetting()

	//初始化ticker
	initUpdatingTicker()

	//检查PAC文件更新
	if setting.GFWListAutoUpdateMode == configure.AutoUpdate ||
		setting.GFWListAutoUpdateMode == configure.AutoUpdateAtIntervals ||
		setting.Transparent == configure.TransparentGfwlist {
		if setting.GFWListAutoUpdateMode == configure.AutoUpdateAtIntervals {
			conf.TickerUpdateGFWList.Reset(time.Duration(setting.GFWListAutoUpdateIntervalHour) * time.Hour)
		}
		switch setting.RulePortMode {
		case configure.GfwlistMode:
			go func() {
				/* 更新LoyalsoldierSite.dat */
				localGFWListVersion, err := dat.CheckAndUpdateGFWList("")
				if err != nil {
					log.Warn("Failed to update PAC file: %v", err.Error())
					return
				}
				log.Info("Complete updating PAC file. Localtime: %v", localGFWListVersion)
			}()
		case configure.CustomMode:
			// obsolete
		}
	}

	//检查订阅更新
	if setting.SubscriptionAutoUpdateMode == configure.AutoUpdate ||
		setting.SubscriptionAutoUpdateMode == configure.AutoUpdateAtIntervals {

		if setting.SubscriptionAutoUpdateMode == configure.AutoUpdateAtIntervals {
			conf.TickerUpdateSubscription.Reset(time.Duration(setting.SubscriptionAutoUpdateIntervalHour) * time.Hour)
		}
		go updateSubscriptions()
	}
	// 检查服务端更新
	go func() {
		f := func() {
			if foundNew, remote, err := service.CheckUpdate(); err == nil {
				conf.FoundNew = foundNew
				conf.RemoteVersion = remote
			}
		}
		f()
		c := time.Tick(7 * 24 * time.Hour)
		for range c {
			f()
		}
	}()
}

func run() (err error) {
	//判别需要启动v2ray吗
	if configure.GetRunning() {
		//configure the ip forward
		setting := service.GetSetting()
		if setting.IpForward && !ipforward.IsIpForwardOn() {
			e := ipforward.WriteIpForward(true)
			if e != nil {
				log.Warn("Connect: %v", e)
			}
		}
		// Start.
		err := v2ray.UpdateV2RayConfig()
		if err != nil {
			log.Error("failed to start v2ray-core: %v", err)
		}
	} else {
		log.Info("the core was not running the last time v2rayA exited")
	}
	//w := configure.GetConnectedServers()
	//log.Println(err, ", which:", w)
	//_ = configure.ClearConnected()
	errch := make(chan error)
	//启动服务端
	go func() {
		errch <- router.Run()
	}()
	//监听信号，处理透明代理的关闭
	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGKILL, syscall.SIGILL)
		<-sigs
		errch <- nil
	}()
	return waitForShutdown(errch, func() {
		fmt.Println("Quitting...")
		v2ray.ProcessManager.CheckAndStopTransparentProxy(nil)
		v2ray.ProcessManager.Stop(false)
	})
}

func waitForShutdown(errch <-chan error, cleanup func()) error {
	defer cleanup()
	if err := <-errch; err != nil {
		return fmt.Errorf("run: %w", err)
	}
	return nil
}
