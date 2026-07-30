package atrust

import (
	"testing"
	"time"
)

func TestConntrackManagerReleasesCompletedAuthLookup(t *testing.T) {
	manager := newConntrackMgr()
	entry := manager.getOrCreate("flow-1", "app", "group")
	manager.markAuth(entry.authID, "token", nil)
	if manager.byID[entry.authID] != nil {
		t.Fatal("completed authentication remained indexed by auth ID")
	}
	if got := manager.getOrCreate("flow-1", "app", "group"); got != entry {
		t.Fatal("completed conntrack was not retained by flow key")
	}
}

func TestConntrackManagerEvictsOldestCompletedEntryAtLimit(t *testing.T) {
	manager := newConntrackMgr()
	manager.limit = 2
	manager.idleAfter = time.Hour
	oldest := manager.getOrCreate("flow-oldest", "app", "group")
	manager.markAuth(oldest.authID, "token", nil)
	oldest.lastUsed.Store(time.Now().Add(-time.Minute).UnixNano())
	pending := manager.getOrCreate("flow-pending", "app", "group")

	newest := manager.getOrCreate("flow-new", "app", "group")
	if manager.byKey["flow-oldest"] != nil {
		t.Fatal("oldest completed conntrack was not evicted")
	}
	if manager.byKey["flow-pending"] != pending {
		t.Fatal("pending conntrack was evicted")
	}
	if manager.byKey["flow-new"] != newest || len(manager.byKey) != 2 {
		t.Fatalf("conntrack entries = %#v", manager.byKey)
	}
}

func TestConntrackManagerDoesNotEvictPendingAuthentication(t *testing.T) {
	manager := newConntrackMgr()
	manager.limit = 1
	pending := manager.getOrCreate("flow-pending", "app", "group")
	manager.getOrCreate("flow-new", "app", "group")
	if manager.byKey["flow-pending"] != pending {
		t.Fatal("pending authentication was evicted")
	}
}
