package configure

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/v2rayA/v2rayA/db"
	"github.com/v2rayA/v2rayA/db/store"
)

var ErrSubscriptionChanged = errors.New("subscription changed while downloading; retry the refresh")

type referenceObject map[string]json.RawMessage

func readReferenceObject(raw []byte, label string) (referenceObject, error) {
	var object referenceObject
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("invalid %s JSON", label)
	}
	return object, nil
}

func readReferenceList(raw []byte, label string) ([]json.RawMessage, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return []json.RawMessage{}, nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("invalid %s list", label)
	}
	return list, nil
}

type referenceTouches struct {
	servers, subscriptions []json.RawMessage
	subServers             [][]json.RawMessage
}

func decodeReferenceServerRaw(raw []byte) (*ServerRaw, error) {
	wrapper, err := readReferenceObject(raw, "node")
	if err != nil {
		return nil, err
	}
	if _, err := readReferenceObject(wrapper["serverObj"], "node configuration"); err != nil {
		return nil, err
	}
	return Bytes2ServerRaw(raw)
}

func readReferenceListInTx(tx store.ReadTx, bucket, key, label string) ([]json.RawMessage, error) {
	raw, err := tx.Get([]byte(bucket), []byte(key))
	if err != nil {
		return nil, err
	}
	return readReferenceList(raw, label)
}

func loadReferenceTouches(tx store.ReadTx) (state referenceTouches, err error) {
	if state.servers, err = readReferenceListInTx(tx, "touch", "servers", "servers"); err != nil {
		return state, err
	}
	if state.subscriptions, err = readReferenceListInTx(tx, "touch", "subscriptions", "subscriptions"); err != nil {
		return state, err
	}
	for _, raw := range state.subscriptions {
		object, err := readReferenceObject(raw, "subscription")
		if err != nil {
			return state, err
		}
		servers, err := readReferenceList(object["servers"], "subscription servers")
		if err != nil {
			return state, err
		}
		state.subServers = append(state.subServers, servers)
	}
	return state, nil
}

func (s referenceTouches) locate(w Which) (json.RawMessage, error) {
	var list []json.RawMessage
	switch w.TYPE {
	case ServerType:
		list = s.servers
	case SubscriptionServerType:
		if w.Sub < 0 || w.Sub >= len(s.subServers) {
			return nil, fmt.Errorf("subscription reference is out of range")
		}
		list = s.subServers[w.Sub]
	default:
		return nil, fmt.Errorf("reference must identify a node")
	}
	if w.ID <= 0 || w.ID > len(list) {
		return nil, fmt.Errorf("node reference is out of range")
	}
	if _, err := nodeReferenceIdentity(list[w.ID-1]); err != nil {
		return nil, err
	}
	return list[w.ID-1], nil
}

// Compare the complete node configuration, including unknown extensions. Decode
// known fields first to apply legacy URI recovery and normal protocol defaults.
// V is a representation/cache field for VMess/VLESS, not a connection setting.
func nodeReferenceIdentity(raw json.RawMessage) (string, error) {
	wrapper, err := readReferenceObject(raw, "node")
	if err != nil {
		return "", err
	}
	original, err := readReferenceObject(wrapper["serverObj"], "node configuration")
	if err != nil {
		return "", err
	}
	parsed, err := decodeReferenceServerRaw(raw)
	if err != nil {
		return "", fmt.Errorf("cannot decode referenced node configuration")
	}
	normalized, err := json.Marshal(parsed.ServerObj)
	if err != nil {
		return "", fmt.Errorf("cannot normalize referenced node configuration")
	}
	object, err := readReferenceObject(normalized, "node configuration")
	if err != nil {
		return "", err
	}
	mergeUnknownReferenceFields(object, original, reflect.TypeOf(parsed.ServerObj))
	var protocol string
	_ = json.Unmarshal(object["protocol"], &protocol)
	if protocol == "vmess" || protocol == "vless" {
		delete(object, "v")
	}
	normalized, err = json.Marshal(object)
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(normalized))
	decoder.UseNumber()
	var value interface{}
	if err = decoder.Decode(&value); err != nil {
		return "", err
	}
	normalized, err = json.Marshal(value)
	return string(normalized), err
}

func mergeUnknownReferenceFields(normalized, original referenceObject, typ reflect.Type) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return
	}
	known := map[string]reflect.Type{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" {
			name = field.Name
		}
		known[name] = field.Type
	}
	for key, value := range original {
		field, exists := known[key]
		if !exists {
			normalized[key] = value
			continue
		}
		if left, err := readReferenceObject(normalized[key], "field"); err == nil {
			if right, err := readReferenceObject(value, "field"); err == nil {
				mergeUnknownReferenceFields(left, right, field)
				normalized[key], _ = json.Marshal(left)
			}
		}
	}
}

type referenceRemap func(Which, bool) (Which, bool, error)

func referenceOutboundNames(tx store.ReadTx, includeOrphans bool) ([]string, error) {
	// Decode the real gob set; scanning printable bytes corrupts short/Unicode names.
	names := map[string]bool{"outbound.proxy": true}
	raw, err := tx.Get([]byte("outbounds"), []byte("names"))
	if err != nil {
		return nil, err
	}
	if raw != nil {
		var set map[[32]byte]interface{}
		if err := gob.NewDecoder(bytes.NewReader(raw)).Decode(&set); err != nil {
			return nil, fmt.Errorf("invalid outbound names set")
		}
		for _, value := range set {
			name, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("invalid outbound name")
			}
			names["outbound."+name] = true
		}
	}
	if includeOrphans {
		if err := tx.ForEachBucket(func(name []byte) error {
			if strings.HasPrefix(string(name), "outbound.") {
				names[string(name)] = true
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	delete(names, "outbound.proxy")
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	return append([]string{"outbound.proxy"}, ordered...), nil
}

func rewriteWhich(raw json.RawMessage, state referenceTouches, remap referenceRemap, docker bool) (json.RawMessage, bool, error) {
	object, err := readReferenceObject(raw, "node reference")
	if err != nil {
		return nil, false, err
	}
	var old Which
	if err = json.Unmarshal(raw, &old); err != nil {
		return nil, false, fmt.Errorf("invalid node reference")
	}
	if _, err = state.locate(old); err != nil {
		return nil, false, err
	}
	next, keep, err := remap(old, docker)
	if err != nil || !keep {
		return nil, keep, err
	}
	if old.ID == next.ID && old.Sub == next.Sub {
		return raw, true, nil
	}
	object["id"], _ = json.Marshal(next.ID)
	if old.Sub != next.Sub {
		object["sub"], _ = json.Marshal(next.Sub)
	}
	updated, err := json.Marshal(object)
	return updated, true, err
}

// All work here uses the caller's transaction; no db helper may open another.
func rewriteReferences(tx store.WriteTx, state referenceTouches, remap referenceRemap) (bool, error) {
	updates := map[string][]byte{}
	var blocked []int
	err := tx.ForEach([]byte("dockerProxies"), func(key, raw []byte) error {
		proxy, err := decodeDockerProxy(key, raw)
		if err != nil {
			return err
		}
		object, _ := readReferenceObject(raw, "Docker proxy")
		keepProxy := true
		for _, field := range []string{"which", "frontWhich"} {
			reference := object[field]
			if field == "frontWhich" && (reference == nil || bytes.Equal(bytes.TrimSpace(reference), []byte("null"))) {
				continue
			}
			updated, keep, err := rewriteWhich(reference, state, remap, true)
			if err != nil {
				return fmt.Errorf("Docker proxy port %d has an invalid or conflicting %s: %w", proxy.Port, field, err)
			}
			keepProxy = keepProxy && keep
			if keep {
				object[field] = updated
			}
		}
		if !keepProxy {
			blocked = append(blocked, proxy.Port)
		}
		updated, err := json.Marshal(object)
		updates[string(key)] = updated
		return err
	})
	if err != nil {
		return false, err
	}
	if len(blocked) != 0 {
		sort.Ints(blocked)
		return false, fmt.Errorf("nodes are used by Docker proxy ports %v; rebind or remove those proxies first", blocked)
	}
	for key, raw := range updates {
		if err := tx.Put([]byte("dockerProxies"), []byte(key), raw); err != nil {
			return false, err
		}
	}
	names, err := referenceOutboundNames(tx, true)
	if err != nil {
		return false, err
	}
	removed := false
	for _, name := range names {
		raw, err := tx.Get([]byte(name), []byte("connectedServers"))
		if err != nil {
			return false, err
		}
		if raw == nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		object, err := readReferenceObject(raw, "outbound connections")
		if err != nil {
			return false, err
		}
		var references []json.RawMessage
		if raw := object["touches"]; raw != nil && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			references, err = readReferenceList(raw, "outbound connections")
			if err != nil {
				return false, err
			}
		}
		kept := make([]json.RawMessage, 0, len(references))
		for _, reference := range references {
			updated, keep, err := rewriteWhich(reference, state, remap, false)
			if err != nil {
				return false, fmt.Errorf("invalid connection in outbound %q: %w", strings.TrimPrefix(name, "outbound."), err)
			}
			if keep {
				kept = append(kept, updated)
			} else {
				removed = true
			}
		}
		object["touches"], _ = json.Marshal(kept)
		updated, err := json.Marshal(object)
		if err != nil {
			return false, err
		}
		if err = tx.Put([]byte(name), []byte("connectedServers"), updated); err != nil {
			return false, err
		}
	}
	return removed, nil
}

// DeleteTouchesAtomic removes standalone nodes/subscriptions and remaps every
// reference in the same transaction. The result is true only if a main-core
// connection was removed; renumbering alone does not require a restart.
func DeleteTouchesAtomic(whiches []*Which) (bool, error) {
	database, err := db.CurrentStore()
	if err != nil {
		return false, err
	}
	return deleteTouchesAtomic(database, whiches)
}

func deleteTouchesAtomic(database store.Store, whiches []*Which) (removed bool, err error) {
	err = database.Update(func(tx store.WriteTx) error {
		state, err := loadReferenceTouches(tx)
		if err != nil {
			return err
		}
		servers, subscriptions := map[int]bool{}, map[int]bool{}
		for _, w := range whiches {
			if w == nil {
				return fmt.Errorf("nil deletion target")
			}
			switch w.TYPE {
			case ServerType:
				if w.ID <= 0 || w.ID > len(state.servers) {
					return fmt.Errorf("server deletion index is out of range")
				}
				servers[w.ID-1] = true
			case SubscriptionType:
				if w.ID <= 0 || w.ID > len(state.subscriptions) {
					return fmt.Errorf("subscription deletion index is out of range")
				}
				subscriptions[w.ID-1] = true
			default:
				return fmt.Errorf("only standalone nodes and whole subscriptions can be deleted")
			}
		}
		if len(servers)+len(subscriptions) == 0 {
			return fmt.Errorf("no nodes or subscriptions selected")
		}
		remap := func(w Which, _ bool) (Which, bool, error) {
			if w.TYPE == ServerType {
				if servers[w.ID-1] {
					return w, false, nil
				}
				w.ID = referenceIndexAfterDeletion(w.ID-1, servers) + 1
			} else {
				if subscriptions[w.Sub] {
					return w, false, nil
				}
				w.Sub = referenceIndexAfterDeletion(w.Sub, subscriptions)
			}
			return w, true, nil
		}
		if removed, err = rewriteReferences(tx, state, remap); err != nil {
			return err
		}
		for key, deletion := range map[string]map[int]bool{"servers": servers, "subscriptions": subscriptions} {
			if len(deletion) == 0 {
				continue
			}
			list := state.servers
			if key == "subscriptions" {
				list = state.subscriptions
			}
			kept := make([]json.RawMessage, 0, len(list)-len(deletion))
			for i, raw := range list {
				if !deletion[i] {
					kept = append(kept, raw)
				}
			}
			raw, err := json.Marshal(kept)
			if err != nil {
				return err
			}
			if err = tx.Put([]byte("touch"), []byte(key), raw); err != nil {
				return err
			}
		}
		return nil
	})
	return removed && err == nil, err
}

func referenceIndexAfterDeletion(index int, deleted map[int]bool) int {
	result := index
	for candidate := range deleted {
		if candidate < index {
			result--
		}
	}
	return result
}

// ReadSubscriptionSnapshot copies the original JSON for a later CAS commit.
func ReadSubscriptionSnapshot(index int) ([]byte, error) {
	database, err := db.CurrentStore()
	if err != nil {
		return nil, err
	}
	return readSubscriptionSnapshot(database, index)
}

func readSubscriptionSnapshot(database store.Store, index int) (snapshot []byte, err error) {
	err = database.View(func(tx store.ReadTx) error {
		state, err := loadReferenceTouches(tx)
		if err != nil {
			return err
		}
		if index < 0 || index >= len(state.subscriptions) {
			return fmt.Errorf("subscription index is out of range")
		}
		snapshot = append([]byte(nil), state.subscriptions[index]...)
		return nil
	})
	return snapshot, err
}

func ReadSubscriptionSnapshots() ([][]byte, error) {
	database, err := db.CurrentStore()
	if err != nil {
		return nil, err
	}
	return readSubscriptionSnapshots(database)
}

func readSubscriptionSnapshots(database store.Store) (snapshots [][]byte, err error) {
	err = database.View(func(tx store.ReadTx) error {
		state, err := loadReferenceTouches(tx)
		if err != nil {
			return err
		}
		for _, raw := range state.subscriptions {
			snapshots = append(snapshots, append([]byte(nil), raw...))
		}
		return nil
	})
	return snapshots, err
}

type ConnectedServerSnapshot struct {
	Which  Which
	Server ServerRaw
}

// ReadConnectedServerSnapshot resolves references and nodes from one database
// version, so a simultaneous delete/reorder cannot select another node.
func ReadConnectedServerSnapshot() ([]ConnectedServerSnapshot, error) {
	database, err := db.CurrentStore()
	if err != nil {
		return nil, err
	}
	return readConnectedServerSnapshot(database)
}

func readConnectedServerSnapshot(database store.Store) (snapshots []ConnectedServerSnapshot, err error) {
	err = database.View(func(tx store.ReadTx) error {
		names, err := referenceOutboundNames(tx, false)
		if err != nil {
			return err
		}
		for _, name := range names {
			raw, err := tx.Get([]byte(name), []byte("connectedServers"))
			if err != nil {
				return err
			}
			if raw == nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				continue
			}
			object, err := readReferenceObject(raw, "outbound connections")
			if err != nil {
				return err
			}
			var references []json.RawMessage
			if raw := object["touches"]; raw != nil && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				references, err = readReferenceList(raw, "outbound connections")
				if err != nil {
					return err
				}
			}
			for _, raw := range references {
				var which Which
				if err := json.Unmarshal(raw, &which); err != nil {
					return fmt.Errorf("invalid outbound connection")
				}
				server, err := locateReferenceServerInTx(tx, which)
				if err != nil {
					return err
				}
				snapshots = append(snapshots, ConnectedServerSnapshot{Which: which, Server: *server})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return snapshots, nil
}

// Decode only the referenced entry. An unused malformed subscription must not
// stop an independent node, or prevent the final connection from being stopped.
func locateReferenceServerInTx(tx store.ReadTx, which Which) (*ServerRaw, error) {
	if which.ID <= 0 || which.TYPE == SubscriptionServerType && which.Sub < 0 {
		return nil, fmt.Errorf("node reference is out of range")
	}
	var nodes []json.RawMessage
	var err error
	switch which.TYPE {
	case ServerType:
		nodes, err = readReferenceListInTx(tx, "touch", "servers", "servers")
	case SubscriptionServerType:
		subscriptions, e := readReferenceListInTx(tx, "touch", "subscriptions", "subscriptions")
		if e != nil {
			return nil, e
		}
		if which.Sub >= len(subscriptions) {
			return nil, fmt.Errorf("subscription reference is out of range")
		}
		subscription, e := readReferenceObject(subscriptions[which.Sub], "subscription")
		if e != nil {
			return nil, e
		}
		nodes, err = readReferenceList(subscription["servers"], "subscription servers")
	default:
		return nil, fmt.Errorf("reference must identify a node")
	}
	if err != nil {
		return nil, err
	}
	if which.ID > len(nodes) {
		return nil, fmt.Errorf("node reference is out of range")
	}
	return decodeReferenceServerRaw(nodes[which.ID-1])
}

func CommitSubscriptionRefresh(index int, expected []byte, servers []ServerRaw, status, info string, disconnectIfNecessary bool) (bool, error) {
	database, err := db.CurrentStore()
	if err != nil {
		return false, err
	}
	return commitSubscriptionRefresh(database, index, expected, servers, status, info, disconnectIfNecessary)
}

func commitSubscriptionRefresh(database store.Store, index int, expected []byte, servers []ServerRaw, status, info string, disconnect bool) (removed bool, err error) {
	newServers := make([]json.RawMessage, 0, len(servers))
	newIdentities := map[string][]int{}
	for i, server := range servers {
		raw, err := json.Marshal(server)
		if err != nil {
			return false, err
		}
		identity, err := nodeReferenceIdentity(raw)
		if err != nil {
			return false, err
		}
		newServers = append(newServers, raw)
		newIdentities[identity] = append(newIdentities[identity], i)
	}
	err = database.Update(func(tx store.WriteTx) error {
		state, err := loadReferenceTouches(tx)
		if err != nil {
			return err
		}
		if index < 0 || index >= len(state.subscriptions) || !bytes.Equal(state.subscriptions[index], expected) {
			return ErrSubscriptionChanged
		}
		oldNodes := state.subServers[index]
		oldIdentities := make([]string, len(oldNodes))
		oldCounts := map[string]int{}
		for i, raw := range oldNodes {
			identity, err := nodeReferenceIdentity(raw)
			if err != nil {
				return err
			}
			oldIdentities[i] = identity
			oldCounts[identity]++
		}
		// Refresh known values from the feed, but retain wrapper extensions for
		// uniquely matched nodes instead of discarding fields unknown to this build.
		for i, identity := range oldIdentities {
			if oldCounts[identity] != 1 || len(newIdentities[identity]) != 1 {
				continue
			}
			index := newIdentities[identity][0]
			old, _ := readReferenceObject(oldNodes[i], "node")
			fresh, _ := readReferenceObject(newServers[index], "node")
			for key, value := range old {
				if _, exists := fresh[key]; !exists {
					fresh[key] = value
				}
			}
			newServers[index], err = json.Marshal(fresh)
			if err != nil {
				return err
			}
		}
		appended := map[int]int{}
		remap := func(w Which, docker bool) (Which, bool, error) {
			if w.TYPE != SubscriptionServerType || w.Sub != index {
				return w, true, nil
			}
			identity := oldIdentities[w.ID-1]
			candidates := newIdentities[identity]
			if docker && (len(candidates) > 1 || oldCounts[identity] > 1) {
				return w, false, fmt.Errorf("subscription contains ambiguous duplicate node configurations")
			}
			if len(candidates) != 0 {
				w.ID = candidates[0] + 1
				return w, true, nil
			}
			if docker || disconnect {
				return w, false, nil
			}
			id, exists := appended[w.ID]
			if !exists {
				newServers = append(newServers, oldNodes[w.ID-1])
				id = len(newServers)
				appended[w.ID] = id
			}
			w.ID = id
			return w, true, nil
		}
		if removed, err = rewriteReferences(tx, state, remap); err != nil {
			return err
		}
		object, _ := readReferenceObject(state.subscriptions[index], "subscription")
		object["servers"], err = json.Marshal(newServers)
		if err != nil {
			return err
		}
		object["status"], _ = json.Marshal(status)
		object["info"], _ = json.Marshal(info)
		state.subscriptions[index], err = json.Marshal(object)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(state.subscriptions)
		if err != nil {
			return err
		}
		return tx.Put([]byte("touch"), []byte("subscriptions"), raw)
	})
	return removed && err == nil, err
}

func decodeDockerProxy(key, raw []byte) (DockerProxy, error) {
	var proxy DockerProxy
	if _, err := readReferenceObject(raw, "Docker proxy"); err != nil {
		return proxy, fmt.Errorf("invalid Docker proxy record for port %q", key)
	}
	if err := json.Unmarshal(raw, &proxy); err != nil || proxy.Port < 1 || proxy.Port > 65535 || strconv.Itoa(proxy.Port) != string(key) {
		return proxy, fmt.Errorf("invalid Docker proxy record for port %q", key)
	}
	if proxy.Which.TYPE != ServerType && proxy.Which.TYPE != SubscriptionServerType || proxy.Which.ID <= 0 || proxy.Which.Sub < 0 {
		return proxy, fmt.Errorf("invalid Docker proxy reference for port %d", proxy.Port)
	}
	if w := proxy.FrontWhich; w != nil && (w.TYPE != ServerType && w.TYPE != SubscriptionServerType || w.ID <= 0 || w.Sub < 0) {
		return proxy, fmt.Errorf("invalid Docker front proxy reference for port %d", proxy.Port)
	}
	return proxy, nil
}
