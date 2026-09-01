//go:build linux

package runner

import (
	"errors"
	"os"
	"os/exec"
	"os/user"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestLookupUserCredentialsResolvesPrimaryAndSupplementaryGroups(t *testing.T) {
	lookup := func(id string) (*user.User, error) {
		if id != "1500" {
			t.Fatalf("lookup id = %q, want 1500", id)
		}
		return &user.User{Uid: "1500", Gid: "1600", Username: "target"}, nil
	}
	groups := func(*user.User) ([]string, error) {
		return []string{"1700", "1600", "1800", "1700"}, nil
	}

	got, err := lookupUserCredentialsWith(1500, lookup, groups)
	if err != nil {
		t.Fatalf("lookup credentials: %v", err)
	}
	if got.uid != 1500 || got.gid != 1600 {
		t.Fatalf("credentials uid/gid = %d/%d, want 1500/1600", got.uid, got.gid)
	}
	if want := []uint32{1600, 1700, 1800}; !reflect.DeepEqual(got.groups, want) {
		t.Fatalf("credentials groups = %v, want %v", got.groups, want)
	}
}

func TestLookupUserCredentialsRejectsIDsOutsideLinuxRange(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("an int cannot represent a uid above uint32 on this architecture")
	}
	tooLarge := uint64(^uint32(0)) + 1
	_, err := lookupUserCredentialsWith(int(tooLarge), func(string) (*user.User, error) {
		t.Fatal("out-of-range uid reached user lookup")
		return nil, nil
	}, func(*user.User) ([]string, error) {
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds Linux credential range") {
		t.Fatalf("out-of-range uid error = %v", err)
	}
}

func TestLookupUserCredentialsRejectsInvalidGroupAndLookupFailures(t *testing.T) {
	lookupErr := errors.New("unknown uid")
	if _, err := lookupUserCredentialsWith(1500, func(string) (*user.User, error) {
		return nil, lookupErr
	}, nil); !errors.Is(err, lookupErr) {
		t.Fatalf("lookup error = %v, want wrapped %v", err, lookupErr)
	}

	_, err := lookupUserCredentialsWith(1500, func(string) (*user.User, error) {
		return &user.User{Uid: "1500", Gid: "1600"}, nil
	}, func(*user.User) ([]string, error) {
		return []string{"4294967296"}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "invalid supplementary gid") {
		t.Fatalf("invalid group error = %v", err)
	}
}

func TestConfigureUserCredentialsChangesOnlyChildCommand(t *testing.T) {
	credentials := &UserCredentials{uid: 1500, gid: 1600, groups: []uint32{1600, 1700}}
	privilegedProxyCommand := exec.Command("proxy")
	targetCommand := exec.Command("target")

	if err := configureUserCredentials(privilegedProxyCommand, nil); err != nil {
		t.Fatalf("configure proxy command: %v", err)
	}
	if privilegedProxyCommand.SysProcAttr != nil {
		t.Fatal("ordinary command received process attributes")
	}
	if err := configureUserCredentials(targetCommand, credentials); err != nil {
		t.Fatalf("configure target command: %v", err)
	}
	credential := targetCommand.SysProcAttr.Credential
	if credential == nil {
		t.Fatal("target command has no credential")
	}
	if credential.Uid != 1500 || credential.Gid != 1600 {
		t.Fatalf("target credential uid/gid = %d/%d, want 1500/1600", credential.Uid, credential.Gid)
	}
	if want := []uint32{1600, 1700}; !reflect.DeepEqual(credential.Groups, want) {
		t.Fatalf("target credential groups = %v, want %v", credential.Groups, want)
	}

	credentials.groups[0] = 0
	if credential.Groups[0] != 1600 {
		t.Fatal("configured child groups alias the mutable resolved identity")
	}
}

func TestConfigureUserCredentialsRejectsMissingGroupReset(t *testing.T) {
	err := configureUserCredentials(exec.Command("target"), &UserCredentials{uid: 1500, gid: 1600})
	if err == nil || !strings.Contains(err.Error(), "no group list") {
		t.Fatalf("missing group reset error = %v", err)
	}
}

func TestStartWithPTYAttachesCredentialsToCommandBeforeLaunch(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	credentials := &UserCredentials{uid: 1500, gid: 1600, groups: []uint32{1600, 1700}}
	stopBeforeLaunch := errors.New("stop before launch")
	var captured *exec.Cmd
	_, err = startWithPTY(
		[]string{executable, "-test.run=^$"},
		[]string{"TEST_ENV=present"},
		credentials,
		func(cmd *exec.Cmd) (*os.File, error) {
			captured = cmd
			return nil, stopBeforeLaunch
		},
	)
	if !errors.Is(err, stopBeforeLaunch) {
		t.Fatalf("startWithPTY error = %v, want wrapped sentinel", err)
	}
	if captured == nil {
		t.Fatal("PTY starter did not receive a command")
	}
	if captured.SysProcAttr == nil || captured.SysProcAttr.Credential == nil {
		t.Fatal("actual command reached PTY start without user credentials")
	}
	got := captured.SysProcAttr.Credential
	if got.Uid != 1500 || got.Gid != 1600 || !reflect.DeepEqual(got.Groups, []uint32{1600, 1700}) {
		t.Fatalf("actual command credential = uid %d gid %d groups %v", got.Uid, got.Gid, got.Groups)
	}
	if !reflect.DeepEqual(captured.Env, []string{"TEST_ENV=present"}) {
		t.Fatalf("actual command env = %v", captured.Env)
	}
}

func TestStartWithPTYLeavesOrdinaryCommandCredentialsUnset(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	stopBeforeLaunch := errors.New("stop before launch")
	var captured *exec.Cmd
	_, err = startWithPTY([]string{executable}, nil, nil, func(cmd *exec.Cmd) (*os.File, error) {
		captured = cmd
		return nil, stopBeforeLaunch
	})
	if !errors.Is(err, stopBeforeLaunch) {
		t.Fatalf("startWithPTY error = %v, want sentinel", err)
	}
	if captured == nil {
		t.Fatal("PTY starter did not receive a command")
	}
	if captured.SysProcAttr != nil {
		t.Fatalf("ordinary command received process attributes: %+v", captured.SysProcAttr)
	}
}
