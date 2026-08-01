//go:build darwin || linux

package resumestate

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

type fileMetadata struct {
	mode fs.FileMode
	uid  int
	gid  int
}

type fileOwner struct {
	uid int
	gid int
}

func inspectFile(path string) (fileMetadata, error) {
	metadata := fileMetadata{mode: 0o600, uid: -1, gid: -1}
	existingOwner := fileOwner{uid: -1, gid: -1}
	if info, err := os.Stat(path); err == nil {
		metadata.mode = info.Mode().Perm()
		existingOwner = ownerFromFileInfo(info)
	} else if !os.IsNotExist(err) {
		return metadata, fmt.Errorf("stat resume state: %w", err)
	}

	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return metadata, fmt.Errorf("stat resume state directory: %w", err)
	}
	directoryOwner := ownerFromFileInfo(directoryInfo)
	owner := selectOwner(os.Geteuid(), os.Getenv("SUDO_UID"), os.Getenv("SUDO_GID"), existingOwner, directoryOwner)
	metadata.uid = owner.uid
	metadata.gid = owner.gid
	return metadata, nil
}

func applyFileMetadata(file *os.File, metadata fileMetadata) error {
	if os.Geteuid() == 0 && metadata.uid >= 0 && metadata.gid >= 0 {
		if err := file.Chown(metadata.uid, metadata.gid); err != nil {
			return fmt.Errorf("set resume state owner: %w", err)
		}
	}
	if err := file.Chmod(metadata.mode); err != nil {
		return fmt.Errorf("set resume state permissions: %w", err)
	}
	return nil
}

func selectOwner(euid int, sudoUID, sudoGID string, existingOwner, directoryOwner fileOwner) fileOwner {
	if euid == 0 {
		uid, uidErr := strconv.Atoi(sudoUID)
		gid, gidErr := strconv.Atoi(sudoGID)
		if uidErr == nil && gidErr == nil && uid >= 0 && gid >= 0 {
			return fileOwner{uid: uid, gid: gid}
		}
	}
	if existingOwner.uid >= 0 && existingOwner.gid >= 0 {
		return existingOwner
	}
	return directoryOwner
}

func ownerFromFileInfo(info os.FileInfo) fileOwner {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileOwner{uid: -1, gid: -1}
	}
	return fileOwner{uid: int(stat.Uid), gid: int(stat.Gid)}
}
