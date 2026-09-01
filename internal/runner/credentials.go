package runner

// UserCredentials is a resolved Unix user identity for a child process. Its
// fields stay private so callers cannot construct a partial identity that
// accidentally retains the parent's primary or supplementary groups.
type UserCredentials struct {
	uid    uint32
	gid    uint32
	groups []uint32
}

// LookupUserCredentials resolves uid's primary group and supplementary groups.
// The returned identity can be passed to StartWithCredentials.
func LookupUserCredentials(uid int) (*UserCredentials, error) {
	return lookupUserCredentials(uid)
}
