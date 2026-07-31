//go:build darwin || linux

package main

import "testing"

func TestSelectResumeStateOwnerPrefersSudoCaller(t *testing.T) {
	owner := selectResumeStateOwner(
		0,
		"501",
		"20",
		fileOwner{uid: 0, gid: 0},
		fileOwner{uid: 502, gid: 20},
	)
	if owner.uid != 501 || owner.gid != 20 {
		t.Fatalf("owner = %d:%d, want 501:20", owner.uid, owner.gid)
	}
}

func TestSelectResumeStateOwnerPreservesExistingOwner(t *testing.T) {
	owner := selectResumeStateOwner(
		0,
		"",
		"",
		fileOwner{uid: 501, gid: 20},
		fileOwner{uid: 502, gid: 20},
	)
	if owner.uid != 501 || owner.gid != 20 {
		t.Fatalf("owner = %d:%d, want 501:20", owner.uid, owner.gid)
	}
}

func TestSelectResumeStateOwnerUsesDirectoryForNewFile(t *testing.T) {
	owner := selectResumeStateOwner(
		0,
		"invalid",
		"invalid",
		fileOwner{uid: -1, gid: -1},
		fileOwner{uid: 502, gid: 20},
	)
	if owner.uid != 502 || owner.gid != 20 {
		t.Fatalf("owner = %d:%d, want 502:20", owner.uid, owner.gid)
	}
}
