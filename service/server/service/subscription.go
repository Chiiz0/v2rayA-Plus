package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	jsoniter "github.com/json-iterator/go"
	"github.com/v2rayA/v2rayA/common"
	"github.com/v2rayA/v2rayA/common/httpClient"
	"github.com/v2rayA/v2rayA/common/resolv"
	"github.com/v2rayA/v2rayA/core/serverObj"
	"github.com/v2rayA/v2rayA/core/touch"
	"github.com/v2rayA/v2rayA/core/v2ray/where"
	"github.com/v2rayA/v2rayA/db/configure"
	"github.com/v2rayA/v2rayA/pkg/util/log"
)

type SIP008 struct {
	Version        int    `json:"version"`
	Username       string `json:"username"`
	UserUUID       string `json:"user_uuid"`
	BytesUsed      uint64 `json:"bytes_used"`
	BytesRemaining uint64 `json:"bytes_remaining"`
	Servers        []struct {
		Server     string `json:"server"`
		ServerPort int    `json:"server_port"`
		Password   string `json:"password"`
		Method     string `json:"method"`
		Plugin     string `json:"plugin"`
		PluginOpts string `json:"plugin_opts"`
		Remarks    string `json:"remarks"`
		ID         string `json:"id"`
	} `json:"servers"`
}

func resolveSIP008(raw string) (infos []serverObj.ServerObj, sip SIP008, err error) {
	err = jsoniter.Unmarshal([]byte(raw), &sip)
	if err != nil {
		return
	}
	for _, server := range sip.Servers {
		u := url.URL{
			Scheme:   "ss",
			User:     url.UserPassword(server.Method, server.Password),
			Host:     net.JoinHostPort(server.Server, strconv.Itoa(server.ServerPort)),
			RawQuery: url.Values{"plugin": []string{server.PluginOpts}}.Encode(),
			Fragment: server.Remarks,
		}
		obj, err := serverObj.NewFromLink("shadowsocks", u.String())
		if err != nil {
			return nil, SIP008{}, err
		}
		infos = append(infos, obj)
	}
	return
}

func resolveByLines(raw string) (infos []serverObj.ServerObj, status string, err error) {
	// 切分raw
	rows := strings.Split(strings.TrimSpace(raw), "\n")
	// 解析
	infos = make([]serverObj.ServerObj, 0)
	for _, row := range rows {
		if strings.HasPrefix(row, "STATUS=") {
			status = strings.TrimPrefix(row, "STATUS=")
			continue
		}
		var data serverObj.ServerObj
		data, err = ResolveURL(row)
		if err != nil {
			if !errors.Is(err, EmptyAddressErr) {
				log.Warn("resolveByLines: %v: %v", err, row)
			}
			err = nil
			continue
		}
		infos = append(infos, data)
	}
	return
}

type SubscriptionUserInfo struct {
	Upload   int64
	Download int64
	Total    int64
	Expire   time.Time
}

func (sui *SubscriptionUserInfo) String() string {
	var outputs []string
	if sui.Download != -1 {
		outputs = append(outputs, fmt.Sprintf("download: %v GB", sui.Download/1e9))
	}
	if sui.Upload != -1 {
		outputs = append(outputs, fmt.Sprintf("upload: %v GB", sui.Upload/1e9))
	}
	if sui.Total != -1 {
		outputs = append(outputs, fmt.Sprintf("total: %v GB", sui.Total/1e9))
	}
	if !sui.Expire.IsZero() {
		outputs = append(outputs, fmt.Sprintf("expire: %v UTC", sui.Expire.Format("2006-01-02 15:04")))
	}
	return strings.Join(outputs, "; ")
}

func parseSubscriptionUserInfo(str string) SubscriptionUserInfo {
	fields := strings.Split(str, ";")
	sui := SubscriptionUserInfo{
		Upload:   -1,
		Download: -1,
		Total:    -1,
		Expire:   time.Time{},
	}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		kv := strings.SplitN(field, "=", 2)
		if len(kv) < 2 {
			continue
		}
		v, e := strconv.ParseInt(kv[1], 10, 64)
		if e != nil {
			continue
		}
		switch kv[0] {
		case "upload":
			sui.Upload = v
		case "download":
			sui.Download = v
		case "total":
			sui.Total = v
		case "expire":
			sui.Expire = time.Unix(v, 0).UTC()
		}
	}
	return sui
}
func trapBOM(fileBytes []byte) []byte {
	trimmedBytes := bytes.Trim(fileBytes, "\xef\xbb\xbf")
	return trimmedBytes
}
func ResolveSubscriptionWithClient(source string, client *http.Client) (infos []serverObj.ServerObj, status string, err error) {
	c := *client
	if c.Timeout < 30*time.Second {
		c.Timeout = 30 * time.Second
	}

	res, err := httpClient.HttpGetUsingSpecificClient(&c, source)
	if err != nil {
		return
	}
	defer res.Body.Close()
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		return nil, "", fmt.Errorf("subscription returned HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, "", err
	}
	// base64 decode. trapBOM due to https://github.com/v2rayA/v2rayA/issues/612
	raw, err := common.Base64StdDecode(string(trapBOM(b)))
	if err != nil {
		raw, _ = common.Base64URLDecode(string(b))
	}
	infos, status, err = ResolveByLines(raw)
	if err != nil {
		return nil, "", err
	}
	subscriptionUserInfo := res.Header.Get("Subscription-Userinfo")
	sui := parseSubscriptionUserInfo(subscriptionUserInfo)
	if len(status) > 0 {
		status = sui.String() + "|" + status
	} else {
		status = sui.String()
	}
	return infos, status, nil
}

func ResolveByLines(raw string) (infos []serverObj.ServerObj, status string, err error) {
	var sip SIP008
	if infos, sip, err = resolveSIP008(raw); err == nil {
		status = getDataUsageStatus(sip.BytesUsed, sip.BytesRemaining)
	} else {
		infos, status, err = resolveByLines(raw)
	}
	return
}

func getDataUsageStatus(bytesUsed, bytesRemaining uint64) (status string) {
	if bytesUsed != 0 {
		status = fmt.Sprintf("Used: %.2f GiB", float64(bytesUsed)/1024/1024/1024)
		if bytesRemaining != 0 {
			status += fmt.Sprintf(" | Remaining: %.2f GiB", float64(bytesRemaining)/1024/1024/1024)
		}
	}
	return
}

// SubscriptionUpdateSnapshot keeps a scheduled refresh tied to the exact
// subscription that was selected, even if earlier entries are later deleted.
type SubscriptionUpdateSnapshot struct {
	Index   int
	Address string
	Raw     []byte
}

func SnapshotSubscriptionsForUpdate() ([]SubscriptionUpdateSnapshot, error) {
	nodeOperationMu.Lock()
	defer nodeOperationMu.Unlock()
	raws, err := configure.ReadSubscriptionSnapshots()
	if err != nil {
		return nil, err
	}
	var snapshots []SubscriptionUpdateSnapshot
	for index, raw := range raws {
		subscription, err := configure.Bytes2SubscriptionRaw(raw)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, SubscriptionUpdateSnapshot{Index: index, Address: subscription.Address, Raw: raw})
	}
	return snapshots, nil
}

func UpdateSubscription(index int, disconnectIfNecessary bool) error {
	nodeOperationMu.Lock()
	snapshot, err := configure.ReadSubscriptionSnapshot(index)
	nodeOperationMu.Unlock()
	if err != nil {
		return fmt.Errorf("UpdateSubscription: %w", err)
	}
	return UpdateSubscriptionSnapshot(index, snapshot, disconnectIfNecessary)
}

func UpdateSubscriptionSnapshot(index int, snapshot []byte, disconnectIfNecessary bool) error {
	// Selecting the download route reads connection state. The actual HTTP
	// request is deliberately outside the node operation lock.
	nodeOperationMu.Lock()
	client := httpClient.GetHttpClientAutomatically()
	nodeOperationMu.Unlock()
	resolv.CheckResolvConf()
	return updateSubscriptionSnapshotWithClient(index, snapshot, disconnectIfNecessary, client)
}

func updateSubscriptionSnapshotWithClient(index int, snapshot []byte, disconnectIfNecessary bool, client *http.Client) error {
	subscription, err := configure.Bytes2SubscriptionRaw(snapshot)
	if err != nil {
		return fmt.Errorf("UpdateSubscription: invalid subscription snapshot: %w", err)
	}
	infos, status, err := ResolveSubscriptionWithClient(subscription.Address, client)
	if err != nil {
		return fmt.Errorf("UpdateSubscription: failed to resolve subscription address: %w", err)
	}
	servers := make([]configure.ServerRaw, len(infos))
	for i, info := range infos {
		servers[i] = configure.ServerRaw{ServerObj: info}
	}

	nodeOperationMu.Lock()
	defer nodeOperationMu.Unlock()
	removed, err := configure.CommitSubscriptionRefresh(index, snapshot, servers, string(touch.NewUpdateStatus()), status, disconnectIfNecessary)
	if err != nil {
		return fmt.Errorf("UpdateSubscription: %w", err)
	}
	// The transaction already remapped retained connections and removed missing
	// ones. Restart only after it commits, never against the old indexes.
	if removed {
		if err := refreshCoreAfterNodeChangeLocked(); err != nil {
			return fmt.Errorf("UpdateSubscription: subscription was updated, but core reload failed: %w", err)
		}
	}
	return nil
}

func ModifySubscriptionRemark(subscription touch.Subscription) (err error) {
	nodeOperationMu.Lock()
	defer nodeOperationMu.Unlock()
	raw := configure.GetSubscription(subscription.ID - 1)
	if raw == nil {
		return fmt.Errorf("failed to find the corresponding subscription")
	}
	raw.Remarks = subscription.Remarks
	raw.Address = subscription.Address
	raw.AutoSelect = subscription.AutoSelect
	return configure.SetSubscription(subscription.ID-1, raw)
}

func SelectServersFromSubscription(index int, shouldDisconnect bool) error {
	nodeOperationMu.Lock()
	defer nodeOperationMu.Unlock()
	return selectServersFromSubscriptionLocked(index, shouldDisconnect)
}

func selectServersFromSubscriptionLocked(index int, shouldDisconnect bool) (err error) {
	subscription := configure.GetSubscription(index)
	if subscription == nil {
		return fmt.Errorf("failed to find the corresponding subscription")
	}
	var subscriptionServer configure.Which
	subscriptionServer.TYPE = "subscriptionServer"
	subscriptionServer.Sub = index // Subscription IDs start with 0
	subscriptionServer.Outbound = "proxy"
	variant, _, err := where.GetV2rayServiceVersion()
	if err != nil {
		log.Warn("Could not figure out if the server is running xray or v2ray -- err: %v", err)
	}

	for i, server := range subscription.Servers {
		subscriptionServer.ID = i + 1 // Server IDs start with 1
		serverObj := server.ServerObj
		serverName := serverObj.GetName()

		// Workaround for partial SS support in v2fly and xray
		isSupported, _ := isSupportedLocked(subscriptionServer)
		if !isSupported {
			log.Info("[AutoSelect] Skipping unsupported server %v", serverName)
			continue
		}

		if shouldDisconnect && variant != where.Xray {
			err := disconnectLocked(subscriptionServer, true)
			if err == nil {
				log.Info("[AutoSelect] Disconnected from server: %v", serverName)
			} else {
				log.Error("[AutoSelect] Failed to disconnect from server: %v", serverName)
				return err
			}
		} else {
			err := connectLocked(&subscriptionServer)
			if err == nil {
				log.Info("[AutoSelect] Automatically selected server: %v", serverName)
				//Xray only supports connecting to one server at a time
				if variant == where.Xray {
					break
				}
			} else {
				log.Error("[AutoSelect] Failed to connect to server: %v", serverName)
				return err
			}
		}
	}
	return nil
}

func AutoSelectServersFromSubscriptions(shouldDisconnect bool) (err error) {
	nodeOperationMu.Lock()
	defer nodeOperationMu.Unlock()
	raws, err := configure.ReadSubscriptionSnapshots()
	if err != nil {
		return err
	}
	variant, _, err := where.GetV2rayServiceVersion()
	if err != nil {
		log.Warn("Could not figure out if the server is running xray or v2ray -- err: %v", err)
	}
	for i, raw := range raws {
		subscription, err := configure.Bytes2SubscriptionRaw(raw)
		if err != nil {
			return err
		}
		if subscription.AutoSelect {
			if shouldDisconnect && variant != where.Xray {
				log.Info("[AutoSelect] Automatically disconnecting servers from subscription: %v", subscription.Address)
				err := selectServersFromSubscriptionLocked(i, true)
				if err != nil {
					log.Error("[AutoSelect] Failed to disconnect servers from subscription: %v", subscription.Address)
					return err
				}
			} else {
				log.Info("[AutoSelect] Automatically selecting servers from subscription: %v", subscription.Address)
				err := selectServersFromSubscriptionLocked(i, false)
				if err != nil {
					log.Error("[AutoSelect] Failed to select servers from subscription: %v", subscription.Address)
					return err
				}
				//Xray only supports connecting to one server at a time,
				//there's no point in selecting servers from multiple subscriptions
				if variant == where.Xray {
					break
				}
			}
		}
	}
	return nil
}
