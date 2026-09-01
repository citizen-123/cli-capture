//go:build linux

package main

import (
	"errors"
	"os"
	"testing"

	"github.com/citizen-123/cli-capture/internal/runner"
)

func TestAutomaticTransparentApplyStartsCredentialedTarget(t *testing.T) {
	resolved, err := runner.LookupUserCredentials(os.Getuid())
	if err != nil {
		t.Fatalf("resolve current test user: %v", err)
	}
	lookedUpUID := -1
	credentials, err := transparentTargetCredentialsWithLookup(
		"127.0.0.1:8081",
		true,
		1500,
		func(uid int) (*runner.UserCredentials, error) {
			lookedUpUID = uid
			return resolved, nil
		},
	)
	if err != nil {
		t.Fatalf("transparentTargetCredentials: %v", err)
	}
	if credentials == nil || lookedUpUID != 1500 {
		t.Fatalf("automatic transparent apply selected credentials %v after looking up uid %d", credentials, lookedUpUID)
	}

	stopBeforeLaunch := errors.New("stop before launch")
	ordinaryCalled := false
	credentialedCalled := false
	_, err = startTarget(
		[]string{"target"},
		[]string{"ENV=value"},
		credentials,
		func([]string, []string) (*runner.Target, error) {
			ordinaryCalled = true
			return nil, nil
		},
		func(argv, env []string, got *runner.UserCredentials) (*runner.Target, error) {
			credentialedCalled = true
			if got != credentials {
				t.Fatal("credentialed runner received a different resolved identity")
			}
			if len(argv) != 1 || argv[0] != "target" || len(env) != 1 || env[0] != "ENV=value" {
				t.Fatalf("credentialed runner got argv %v env %v", argv, env)
			}
			return nil, stopBeforeLaunch
		},
	)
	if !errors.Is(err, stopBeforeLaunch) {
		t.Fatalf("startTarget error = %v, want sentinel", err)
	}
	if ordinaryCalled {
		t.Fatal("automatic transparent apply called the ordinary runner path")
	}
	if !credentialedCalled {
		t.Fatal("automatic transparent apply did not call the credentialed runner path")
	}
}
