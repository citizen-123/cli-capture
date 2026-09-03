//go:build linux

package runner

import (
	"fmt"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

func lookupUserCredentials(uid int) (*UserCredentials, error) {
	return lookupUserCredentialsWith(uid, user.LookupId, func(u *user.User) ([]string, error) {
		return u.GroupIds()
	})
}

func lookupUserCredentialsWith(
	uid int,
	lookup func(string) (*user.User, error),
	groupIDs func(*user.User) ([]string, error),
) (*UserCredentials, error) {
	if uid < 0 {
		return nil, fmt.Errorf("uid must be non-negative")
	}
	if uint64(uid) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("uid %d exceeds Linux credential range", uid)
	}

	uidText := strconv.FormatUint(uint64(uid), 10)
	u, err := lookup(uidText)
	if err != nil {
		return nil, fmt.Errorf("resolve uid %s: %w", uidText, err)
	}
	resolvedUID, err := parseCredentialID("uid", u.Uid)
	if err != nil {
		return nil, err
	}
	if resolvedUID != uint32(uid) {
		return nil, fmt.Errorf("uid lookup for %s returned uid %s", uidText, u.Uid)
	}
	if u.Username == "" {
		return nil, fmt.Errorf("uid %s has no username", uidText)
	}
	if u.HomeDir == "" || !filepath.IsAbs(u.HomeDir) {
		return nil, fmt.Errorf("uid %s has invalid home directory %q", uidText, u.HomeDir)
	}
	gid, err := parseCredentialID("primary gid", u.Gid)
	if err != nil {
		return nil, err
	}
	groupTexts, err := groupIDs(u)
	if err != nil {
		return nil, fmt.Errorf("resolve groups for uid %s: %w", uidText, err)
	}

	// A non-empty Groups slice makes os/exec call setgroups in the child. Start
	// with the primary GID so even an incomplete group database result cannot
	// leave the privileged parent's supplementary groups in place.
	groups := make([]uint32, 0, len(groupTexts)+1)
	groups = append(groups, gid)
	seen := map[uint32]struct{}{gid: {}}
	for _, groupText := range groupTexts {
		groupID, err := parseCredentialID("supplementary gid", groupText)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[groupID]; ok {
			continue
		}
		seen[groupID] = struct{}{}
		groups = append(groups, groupID)
	}
	return &UserCredentials{
		uid:      resolvedUID,
		gid:      gid,
		groups:   groups,
		username: u.Username,
		home:     filepath.Clean(u.HomeDir),
	}, nil
}

func parseCredentialID(kind, value string) (uint32, error) {
	id, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", kind, value, err)
	}
	return uint32(id), nil
}

func configureUserCredentials(cmd *exec.Cmd, credentials *UserCredentials) error {
	if credentials == nil {
		return nil
	}
	if len(credentials.groups) == 0 {
		return fmt.Errorf("user credentials have no group list")
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{
		Uid:    credentials.uid,
		Gid:    credentials.gid,
		Groups: append([]uint32(nil), credentials.groups...),
	}
	return nil
}
