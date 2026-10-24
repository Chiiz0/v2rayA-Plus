package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/gob"
	"encoding/json"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Issue struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Location string `json:"location"`
	Message  string `json:"message"`
}

type Counts struct {
	Buckets             int `json:"buckets"`
	Values              int `json:"values"`
	Accounts            int `json:"accounts"`
	Servers             int `json:"servers"`
	Subscriptions       int `json:"subscriptions"`
	SubscriptionServers int `json:"subscriptionServers"`
	Outbounds           int `json:"outbounds"`
	MainConnections     int `json:"mainConnections"`
	DockerProxies       int `json:"dockerProxies"`
}

type DockerReference struct {
	Port          int    `json:"port"`
	ContainerName string `json:"containerName"`
	VolumeName    string `json:"volumeName"`
	Core          string `json:"core"`
	Status        string `json:"status"`
}

type PreservedEntry struct {
	Location string `json:"location"`
	Kind     string `json:"kind"`
}

type Report struct {
	Scope     string            `json:"scope"`
	Issues    []Issue           `json:"issues"`
	Counts    Counts            `json:"counts"`
	Docker    []DockerReference `json:"docker"`
	Preserved []PreservedEntry  `json:"preserved"`
}

func (r Report) HasErrors() bool {
	for _, issue := range r.Issues {
		if issue.Severity == "error" {
			return true
		}
	}
	return false
}

type auditState struct {
	report     Report
	servers    []bool
	subServers [][]bool
	subsValid  []bool
	outbounds  map[string]bool
	containers map[string]bool
}

// Audit never loads application configuration or invokes a core or Docker. The
// archive's raw values, including credentials and unknown extensions, are not
// modified or included in diagnostic messages.
func Audit(archive *Archive) Report {
	a := auditState{
		report: Report{
			Scope:  "Structural JSON/gob and positional reference checks only. Core protocol compatibility and actual Docker container/volume existence or runtime state are not verified. Volume names are inferred from the proxy port; raw backup data is unchanged.",
			Issues: []Issue{}, Docker: []DockerReference{}, Preserved: []PreservedEntry{},
		},
		outbounds: map[string]bool{"proxy": true}, containers: map[string]bool{},
	}
	if archive == nil {
		a.issue("error", "archive_invalid", "/", "Archive is missing.")
		return a.report
	}
	if err := Validate(archive); err != nil {
		// Validation errors can contain arbitrary archive names. Do not echo
		// those messages into a report intended to be shared.
		a.issue("error", "archive_invalid", "/", "Archive format or integrity validation failed.")
		return a.report
	}
	byName := make(map[string]*Bucket)
	for i := range archive.Buckets {
		bucket := &archive.Buckets[i]
		byName[string(bucket.Name)] = bucket
		a.inventory(bucket)
	}
	if bucket := byName["touch"]; bucket != nil {
		a.auditTouch(bucket)
	}
	if bucket := byName["outbounds"]; bucket != nil {
		a.auditOutboundNames(bucket)
	}
	for i := range archive.Buckets {
		bucket := &archive.Buckets[i]
		name := string(bucket.Name)
		location := "/" + auditComponent(bucket.Name)
		switch {
		case name == "touch", name == "outbounds":
			// These were read first so later references see the original indexes.
		case name == "system":
			a.auditSystem(bucket)
		case name == "accounts":
			for _, value := range bucket.Values {
				a.report.Counts.Accounts++
				if !auditJSONKind(value.Data, 's', false) {
					a.issue("error", "account_value_invalid", auditValueLocation(bucket, value), "Account value must be a JSON string; its contents are not inspected or reported.")
				}
			}
		case strings.HasPrefix(name, "outbound."):
			a.auditOutbound(bucket)
		case name == "dockerProxies":
			a.auditDocker(bucket)
		default:
			a.preserve(location, "bucket")
		}
		if name == "system" || name == "accounts" || name == "touch" || name == "outbounds" || name == "dockerProxies" || strings.HasPrefix(name, "outbound.") {
			for _, child := range bucket.Buckets {
				childLocation := location + "/" + auditComponent(child.Name)
				if auditKnownValueKey(name, string(child.Name)) {
					a.report.Preserved = append(a.report.Preserved, PreservedEntry{Location: childLocation, Kind: "bucket"})
					a.issue("error", "known_value_is_bucket", childLocation, "Known application value is stored as a nested bucket; its contents are preserved but cannot be read as that value.")
				} else {
					a.preserve(childLocation, "bucket")
				}
			}
		}
	}
	for name := range a.outbounds {
		if name != "proxy" && byName["outbound."+name] == nil {
			a.issue("warning", "outbound_bucket_missing", "/"+auditComponent([]byte("outbound."+name)), "A registered outbound has no settings or connection bucket; application defaults will apply.")
		}
	}
	a.report.Counts.Outbounds = len(a.outbounds)
	sort.Slice(a.report.Issues, func(i, j int) bool {
		left, right := a.report.Issues[i], a.report.Issues[j]
		return left.Location+"\x00"+left.Code+"\x00"+left.Severity < right.Location+"\x00"+right.Code+"\x00"+right.Severity
	})
	sort.Slice(a.report.Preserved, func(i, j int) bool { return a.report.Preserved[i].Location < a.report.Preserved[j].Location })
	sort.Slice(a.report.Docker, func(i, j int) bool { return a.report.Docker[i].Port < a.report.Docker[j].Port })
	return a.report
}

func auditComponent(value []byte) string {
	if !utf8.Valid(value) {
		return "base64:" + base64.RawURLEncoding.EncodeToString(value)
	}
	return url.PathEscape(string(value))
}

func auditValueLocation(bucket *Bucket, value Value) string {
	return "/" + auditComponent(bucket.Name) + "/" + auditComponent(value.Key)
}

func (a *auditState) inventory(bucket *Bucket) {
	a.report.Counts.Buckets++
	a.report.Counts.Values += len(bucket.Values)
	for i := range bucket.Buckets {
		a.inventory(&bucket.Buckets[i])
	}
}

func (a *auditState) issue(severity, code, location, message string) {
	a.report.Issues = append(a.report.Issues, Issue{Severity: severity, Code: code, Location: location, Message: message})
}

func (a *auditState) preserve(location, kind string) {
	a.report.Preserved = append(a.report.Preserved, PreservedEntry{Location: location, Kind: kind})
	a.issue("warning", "unknown_preserved", location, "Unknown data is preserved without interpreting or printing its raw value.")
}

func auditObject(raw []byte) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, false
	}
	return object, true
}

func auditList(raw []byte, optional bool) ([]json.RawMessage, bool) {
	if optional && (len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))) {
		return nil, true
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	return list, true
}

func auditJSONKind(raw []byte, kind byte, nullable bool) bool {
	if !json.Valid(raw) {
		return false
	}
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		return nullable
	}
	switch kind {
	case 'o':
		return raw[0] == '{'
	case 's':
		return raw[0] == '"'
	case 'b':
		return bytes.Equal(raw, []byte("true")) || bytes.Equal(raw, []byte("false"))
	}
	return false
}

func (a *auditState) auditSystem(bucket *Bucket) {
	for _, value := range bucket.Values {
		location := auditValueLocation(bucket, value)
		kind, known := auditSystemKind(string(value.Key))
		if !known {
			a.preserve(location, "value")
		} else if !auditJSONKind(value.Data, kind, true) {
			a.issue("error", "system_json_invalid", location, "Known system value is malformed JSON or has the wrong top-level type.")
		}
	}
}

func auditSystemKind(key string) (byte, bool) {
	switch key {
	case "setting", "ports", "customPac", "tproxyWhiteIpGroups":
		return 'o', true
	case "internalDnsList", "externalDnsList", "routingA", "domainsExcluded":
		return 's', true
	case "running":
		return 'b', true
	}
	return 0, false
}

func auditKnownValueKey(bucket, key string) bool {
	switch bucket {
	case "system":
		_, known := auditSystemKind(key)
		return known
	case "touch":
		return key == "servers" || key == "subscriptions"
	case "outbounds":
		return key == "names"
	case "accounts", "dockerProxies":
		// The application enumerates every entry here as an account or proxy.
		return true
	}
	return strings.HasPrefix(bucket, "outbound.") && (key == "connectedServers" || key == "setting")
}

func (a *auditState) auditServer(raw []byte, location string) bool {
	object, valid := auditObject(raw)
	if !valid {
		a.issue("error", "server_invalid", location, "Node entry must be a JSON object.")
		return false
	}
	if _, valid = auditObject(object["serverObj"]); !valid {
		a.issue("error", "server_object_invalid", location+"/serverObj", "Node configuration must be a JSON object; protocol fields are not otherwise validated.")
		return false
	}
	return true
}

func (a *auditState) auditTouch(bucket *Bucket) {
	for _, value := range bucket.Values {
		location := auditValueLocation(bucket, value)
		key := string(value.Key)
		if key != "servers" && key != "subscriptions" {
			a.preserve(location, "value")
			continue
		}
		list, valid := auditList(value.Data, true)
		if len(value.Data) == 0 || !valid {
			a.issue("error", "touch_list_invalid", location, "Node and subscription storage must be a JSON array or null.")
			continue
		}
		if key == "servers" {
			a.report.Counts.Servers = len(list)
			a.servers = make([]bool, len(list))
			for i, raw := range list {
				a.servers[i] = a.auditServer(raw, location+"/"+strconv.Itoa(i))
			}
			continue
		}
		a.report.Counts.Subscriptions = len(list)
		a.subServers = make([][]bool, len(list))
		a.subsValid = make([]bool, len(list))
		for i, raw := range list {
			entryLocation := location + "/" + strconv.Itoa(i)
			subscription, valid := auditObject(raw)
			if !valid {
				a.issue("error", "subscription_invalid", entryLocation, "Subscription entry must be a JSON object.")
				continue
			}
			if address, exists := subscription["address"]; exists && !auditJSONKind(address, 's', true) {
				a.issue("error", "subscription_address_invalid", entryLocation+"/address", "Subscription address must be a JSON string.")
			}
			servers, valid := auditList(subscription["servers"], true)
			if !valid {
				a.issue("error", "subscription_servers_invalid", entryLocation+"/servers", "Subscription nodes must be a JSON array or null.")
				continue
			}
			a.subsValid[i] = true
			a.subServers[i] = make([]bool, len(servers))
			a.report.Counts.SubscriptionServers += len(servers)
			for j, server := range servers {
				a.subServers[i][j] = a.auditServer(server, entryLocation+"/servers/"+strconv.Itoa(j))
			}
		}
	}
}

func (a *auditState) auditOutboundNames(bucket *Bucket) {
	for _, value := range bucket.Values {
		location := auditValueLocation(bucket, value)
		if string(value.Key) != "names" {
			a.preserve(location, "value")
			continue
		}
		var names map[[32]byte]interface{}
		decoder := gob.NewDecoder(bytes.NewReader(value.Data))
		if decoder.Decode(&names) != nil {
			a.issue("error", "outbound_names_invalid", location, "Outbound names must be the application's gob map[[32]byte]interface{} string set.")
			continue
		}
		var trailing interface{}
		if decoder.Decode(&trailing) != io.EOF {
			a.issue("error", "outbound_names_trailing", location, "Outbound names contain unexpected trailing gob data.")
		}
		for storedHash, value := range names {
			name, valid := value.(string)
			if !valid {
				a.issue("error", "outbound_name_invalid", location, "Outbound set contains a value that is not a string.")
				continue
			}
			var encoded bytes.Buffer
			_ = gob.NewEncoder(&encoded).Encode(name)
			if sha256.Sum256(encoded.Bytes()) != storedHash {
				a.issue("error", "outbound_name_hash_mismatch", location, "Outbound set key does not match the encoded name; removing or replacing the name may fail.")
			}
			if a.outbounds[name] {
				a.issue("warning", "outbound_name_duplicate", location, "Outbound set contains a duplicate or built-in default name.")
			}
			a.outbounds[name] = true
		}
	}
}

type auditWhich struct {
	Type     string `json:"_type"`
	ID       int    `json:"id"`
	Sub      int    `json:"sub"`
	Outbound string `json:"outbound"`
}

func (a *auditState) auditReference(raw []byte, location string) (auditWhich, bool) {
	var which auditWhich
	if _, valid := auditObject(raw); !valid || json.Unmarshal(raw, &which) != nil {
		a.issue("error", "reference_invalid", location, "Node reference must be an object with valid integer indexes.")
		return which, false
	}
	var nodes []bool
	switch which.Type {
	case "server":
		nodes = a.servers
	case "subscriptionServer":
		if which.Sub < 0 || which.Sub >= len(a.subServers) || !a.subsValid[which.Sub] {
			a.issue("error", "reference_subscription_missing", location, "Node reference identifies a missing or structurally invalid subscription.")
			return which, false
		}
		nodes = a.subServers[which.Sub]
	default:
		a.issue("error", "reference_type_invalid", location, "Node reference type must be server or subscriptionServer.")
		return which, false
	}
	if which.ID <= 0 || which.ID > len(nodes) || !nodes[which.ID-1] {
		a.issue("error", "reference_node_missing", location, "Node reference identifies a missing or structurally invalid node.")
		return which, false
	}
	return which, true
}

func (a *auditState) auditOutbound(bucket *Bucket) {
	name := strings.TrimPrefix(string(bucket.Name), "outbound.")
	if !a.outbounds[name] {
		a.issue("warning", "outbound_orphan", "/"+auditComponent(bucket.Name), "Outbound bucket is not in the registered names set; its connections are still checked and preserved.")
	}
	for _, value := range bucket.Values {
		location := auditValueLocation(bucket, value)
		switch string(value.Key) {
		case "setting":
			if !auditJSONKind(value.Data, 'o', true) {
				a.issue("error", "outbound_setting_invalid", location, "Outbound setting must be a JSON object or null.")
			}
		case "connectedServers":
			if bytes.Equal(bytes.TrimSpace(value.Data), []byte("null")) {
				continue
			}
			object, valid := auditObject(value.Data)
			if !valid {
				a.issue("error", "connections_invalid", location, "Connection set must be a JSON object or null.")
				continue
			}
			touches, valid := auditList(object["touches"], true)
			if !valid {
				a.issue("error", "connections_invalid", location+"/touches", "Connected node references must be a JSON array or null.")
				continue
			}
			a.report.Counts.MainConnections += len(touches)
			for i, raw := range touches {
				entryLocation := location + "/touches/" + strconv.Itoa(i)
				which, valid := a.auditReference(raw, entryLocation)
				outbound := which.Outbound
				if outbound == "" {
					outbound = "proxy"
				}
				if valid && outbound != name {
					a.issue("error", "reference_outbound_mismatch", entryLocation+"/outbound", "Connection outbound name disagrees with its containing bucket.")
				}
			}
		default:
			a.preserve(location, "value")
		}
	}
}

func (a *auditState) auditDocker(bucket *Bucket) {
	for _, value := range bucket.Values {
		a.report.Counts.DockerProxies++
		location := auditValueLocation(bucket, value)
		object, valid := auditObject(value.Data)
		var record struct {
			Port          int    `json:"port"`
			ContainerName string `json:"containerName"`
			Core          string `json:"core"`
			Status        string `json:"status"`
		}
		if !valid || json.Unmarshal(value.Data, &record) != nil {
			a.issue("error", "docker_record_invalid", location, "Docker proxy record must be a JSON object with correctly typed metadata.")
			continue
		}
		if record.Port < 1 || record.Port > 65535 || string(value.Key) != strconv.Itoa(record.Port) {
			a.issue("error", "docker_port_mismatch", location, "Docker proxy port must be between 1 and 65535 and match its decimal storage key.")
		}
		which, targetValid := a.auditReference(object["which"], location+"/which")
		if targetValid && which.Sub < 0 {
			a.issue("error", "docker_reference_invalid", location+"/which", "Docker proxy references cannot contain a negative subscription index.")
		}
		if raw, exists := object["frontWhich"]; exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			front, frontValid := a.auditReference(raw, location+"/frontWhich")
			if frontValid && front.Sub < 0 {
				a.issue("error", "docker_reference_invalid", location+"/frontWhich", "Docker proxy references cannot contain a negative subscription index.")
			}
			if targetValid && frontValid && which.Type == front.Type && which.ID == front.ID && (which.Type == "server" || which.Sub == front.Sub) {
				a.issue("error", "docker_self_front", location+"/frontWhich", "Docker proxy target and front proxy refer to the same node.")
			}
		}
		expectedName := "v2raya-socks-" + strconv.Itoa(record.Port)
		if record.ContainerName == "" {
			a.issue("warning", "docker_container_missing", location+"/containerName", "No container name is recorded; runtime commands derive it from the proxy port.")
		} else {
			if record.ContainerName != expectedName {
				a.issue("error", "docker_container_mismatch", location+"/containerName", "Recorded container name disagrees with the name derived from the proxy port.")
			}
			if a.containers[record.ContainerName] {
				a.issue("error", "docker_container_duplicate", location+"/containerName", "Multiple Docker proxy records claim the same container.")
			}
			a.containers[record.ContainerName] = true
		}
		if record.Core != "" && record.Core != "auto" && record.Core != "xray" && record.Core != "v2fly" {
			a.issue("warning", "docker_core_unknown", location+"/core", "Unknown core selector is preserved; core compatibility is not checked.")
		}
		a.report.Docker = append(a.report.Docker, DockerReference{Port: record.Port, ContainerName: record.ContainerName, VolumeName: expectedName, Core: record.Core, Status: record.Status})
	}
}
