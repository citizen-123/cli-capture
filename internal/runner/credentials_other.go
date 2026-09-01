//go:build !linux

package runner

import (
	"fmt"
	"os/exec"
)

func lookupUserCredentials(uid int) (*UserCredentials, error) {
	return nil, fmt.Errorf("target user credentials are supported only on Linux")
}

func configureUserCredentials(cmd *exec.Cmd, credentials *UserCredentials) error {
	if credentials != nil {
		return fmt.Errorf("target user credentials are supported only on Linux")
	}
	return nil
}
