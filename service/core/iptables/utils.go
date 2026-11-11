package iptables

import (
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/v2rayA/v2rayA/common"
	"github.com/v2rayA/v2rayA/common/cmds"
	"github.com/v2rayA/v2rayA/common/parseGeoIP"
	"github.com/v2rayA/v2rayA/conf"
	"github.com/v2rayA/v2rayA/db/configure"
	"golang.org/x/net/nettest"
)

func IPNet2CIDR(ipnet *net.IPNet) string {
	ones, _ := ipnet.Mask.Size()
	return ipnet.IP.String() + "/" + strconv.Itoa(ones)
}

func GetLocalCIDR() ([]string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var cidrs []string
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok {
			cidrs = append(cidrs, IPNet2CIDR(ipnet))
		}
	}
	return cidrs, nil
}

func IsNft() bool {
	if _, isNft := Redirect.(*nftRedirect); isNft {
		return true
	}
	return false
}

func IsIPv6Supported() bool {
	switch conf.GetEnvironmentConfig().IPV6Support {
	case "on":
		return true
	case "off":
		return false
	default:
	}
	if common.IsDocker() {
		return false
	}
	if !nettest.SupportsIPv6() {
		return false
	} else if runtime.GOOS != "linux" {
		return true
	}
	if IsNft() {
		return true
	}
	return cmds.IsCommandValid("ip6tables") || cmds.IsCommandValid("ip6tables-nft")
}

var (
	commandAvailable = cmds.IsCommandValid
	commandOutput    = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).Output()
	}
	commandCombinedOutput = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput()
	}
)

func IsNftablesSupported() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	return isNftablesSupported(conf.GetEnvironmentConfig().NftablesSupport, common.IsDocker())
}

func isNftablesSupported(nftablesSupport string, docker bool) bool {
	switch nftablesSupport {
	// Warning:
	// This is an experimental feature for nftables support.
	// Auto detection is conservative; explicit on/off always takes precedence.
	case "on":
		return true
	case "off":
		return false
	default:
	}
	if docker {
		return false
	}
	if !commandAvailable("nft") {
		// No nft.
		return false
	}
	out, err := commandOutput("iptables", "--version")
	if err != nil {
		// No iptables.
		return true
	}
	version := string(out)
	if strings.Contains(version, "nf_tables") {
		return true
	}
	if !strings.Contains(version, "(legacy)") {
		return false
	}

	// Listing is read-only. Some kernels, notably PREEMPT_RT kernels built
	// without NETFILTER_XTABLES_LEGACY, have a legacy iptables binary but no
	// legacy mangle table. Only that specific failure justifies using nft;
	// permission errors and other failures do not indicate missing support.
	out, err = commandCombinedOutput("iptables", "-t", "mangle", "-L", "-n")
	if err == nil {
		return false
	}
	message := string(out)
	return strings.Contains(message, "can't initialize iptables table") &&
		strings.Contains(message, "mangle") &&
		strings.Contains(message, "Table does not exist")
}

func GetWhiteListIPs() ([]string, []string) {
	dataModal := configure.GetTproxyWhiteIpGroups()

	var ipv4List []string
	var ipv6List []string
	for _, cc := range dataModal.CountryCodes {
		ipv4s, ipv6s, _ := parseGeoIP.Parser("geoip.dat", cc)
		ipv4List = append(ipv4List, ipv4s...)
		ipv6List = append(ipv6List, ipv6s...)
	}
	for _, v := range dataModal.CustomIps {
		if strings.Contains(v, ":") {
			ipv6List = append(ipv6List, v)
		} else {
			ipv4List = append(ipv4List, v)
		}
	}
	return ipv4List, ipv6List
}

func IsEnabledTproxyWhiteIpGroups() bool {
	ipv4List, ipv6List := GetWhiteListIPs()
	return len(ipv4List) > 0 && len(ipv6List) > 0
}
