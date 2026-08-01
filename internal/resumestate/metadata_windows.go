//go:build windows

package resumestate

import "os"

type fileMetadata struct{}

func inspectFile(string) (fileMetadata, error) {
	return fileMetadata{}, nil
}

func applyFileMetadata(*os.File, fileMetadata) error {
	return nil
}
