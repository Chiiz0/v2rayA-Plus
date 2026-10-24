package resolv

import (
	"github.com/v2rayA/v2rayA/conf"
	"os"
	"runtime"
	"strings"
)

const resolvConf = "/etc/resolv.conf"

func WriteResolvConf(servers []string) {
	if runtime.GOOS != "linux" {
		return
	}
	_ = createResolvConf(resolvConf, servers)
}

// createResolvConf only fills an absent path. O_EXCL also rejects a symlink
// created after the absence check, including one with a missing target.
func createResolvConf(path string, servers []string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	var sb strings.Builder
	for _, server := range servers {
		sb.WriteString("nameserver " + server + "\n")
	}
	if _, err := f.WriteString(sb.String()); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func CheckResolvConf() {
	if runtime.GOOS != "linux" || conf.GetEnvironmentConfig().Lite {
		return
	}
	_ = checkResolvConf(resolvConf)
}

func checkResolvConf(path string) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return createResolvConf(path, []string{"223.6.6.6"})
	} else {
		return err
	}
}
