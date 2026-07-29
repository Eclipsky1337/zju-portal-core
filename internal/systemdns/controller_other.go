//go:build !darwin

package systemdns

func newPlatformController(string) Controller { return nil }
