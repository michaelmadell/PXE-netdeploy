//go:build !windows

package main

import "errors"

// runService: only Windows has a service manager to talk to; systemd runs
// the server as an ordinary process.
func runService(options) (bool, error) { return false, nil }

func serviceCommand([]string) error {
	return errors.New("netdeployd service: Windows only; on Linux the package's systemd unit runs netdeployd (systemctl start netdeploy)")
}
