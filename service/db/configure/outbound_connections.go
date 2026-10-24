package configure

import (
	"encoding/json"
	"fmt"

	"github.com/v2rayA/v2rayA/db"
	"github.com/v2rayA/v2rayA/db/store"
)

// ReplaceOutboundConnections validates and replaces one existing group's entire
// membership in one transaction. A zero limit permits any number of members.
// Callers serialize this operation with node deletion and subscription refresh.
func ReplaceOutboundConnections(outbound string, touches []*Which, limit int) (*OutboundConnectionsSnapshot, error) {
	database, err := db.CurrentStore()
	if err != nil {
		return nil, err
	}
	return replaceOutboundConnections(database, outbound, touches, limit)
}

// OutboundConnectionsSnapshot retains the original bytes, including unknown
// fields and an absent key. Restore must run under the same node-operation lock
// as the replacement, before allowing a concurrent edit of this group.
type OutboundConnectionsSnapshot struct {
	*Whiches
	bucket string
	raw    []byte
}

func (s *OutboundConnectionsSnapshot) Restore() error {
	database, err := db.CurrentStore()
	if err != nil {
		return err
	}
	return s.restore(database)
}

func (s *OutboundConnectionsSnapshot) restore(database store.Store) error {
	return database.Update(func(tx store.WriteTx) error {
		if s.raw == nil {
			return tx.Delete([]byte(s.bucket), []byte("connectedServers"))
		}
		return tx.Put([]byte(s.bucket), []byte("connectedServers"), s.raw)
	})
}

func replaceOutboundConnections(database store.Store, outbound string, touches []*Which, limit int) (previous *OutboundConnectionsSnapshot, err error) {
	if outbound == "" {
		outbound = "proxy"
	}
	err = database.Update(func(tx store.WriteTx) error {
		names, err := referenceOutboundNames(tx, false)
		if err != nil {
			return err
		}
		bucket := "outbound." + outbound
		found := false
		for _, name := range names {
			found = found || name == bucket
		}
		if !found {
			return fmt.Errorf("outbound %q does not exist", outbound)
		}
		state, err := loadReferenceTouches(tx)
		if err != nil {
			return err
		}
		next := &Whiches{Touches: make([]*Which, 0, len(touches))}
		seen := map[Which]bool{}
		for _, touch := range touches {
			if touch == nil {
				return fmt.Errorf("node reference cannot be null")
			}
			if touch.Outbound != "" && touch.Outbound != outbound {
				return fmt.Errorf("node reference belongs to a different outbound")
			}
			if _, err := state.locate(*touch); err != nil {
				return err
			}
			// Latency and exported links are response-only values, not identity.
			w := Which{TYPE: touch.TYPE, ID: touch.ID, Sub: touch.Sub, Outbound: outbound}
			if w.TYPE == ServerType {
				w.Sub = 0
			}
			if !seen[w] {
				seen[w] = true
				next.Add(w)
			}
		}
		if limit > 0 && next.Len() > limit {
			return fmt.Errorf("the selected core permits at most %d server per outbound", limit)
		}
		raw, err := tx.Get([]byte(bucket), []byte("connectedServers"))
		if err != nil {
			return err
		}
		previous = &OutboundConnectionsSnapshot{Whiches: &Whiches{Touches: make([]*Which, 0)}, bucket: bucket, raw: raw}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, previous.Whiches); err != nil {
				return fmt.Errorf("invalid existing outbound connections: %w", err)
			}
		}
		encoded, err := json.Marshal(next)
		if err != nil {
			return err
		}
		return tx.Put([]byte(bucket), []byte("connectedServers"), encoded)
	})
	return previous, err
}
