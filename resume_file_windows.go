//go:build windows

package main

import "os"

type resumeStateFileMetadata struct{}

func inspectResumeStateFile(string) (resumeStateFileMetadata, error) {
	return resumeStateFileMetadata{}, nil
}

func applyResumeStateFileMetadata(*os.File, resumeStateFileMetadata) error {
	return nil
}
