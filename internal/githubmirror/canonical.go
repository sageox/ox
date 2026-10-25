package githubmirror

import "strings"

// CanonicalRepo returns the owner and name GitHub currently gives the repo the
// git remote names, as the relay recorded them in its state file. A renamed or
// transferred repo keeps working through its old remote URL while the relay
// publishes posts under GitHub's current name, so a reader that trusts only
// the remote's spelling looks for the wrong posts.
//
// It reads the local state file and never the network, so prime can call it on
// its hot path. The recorded name is used only when the state was written for
// this remote (the same case-insensitive match the relay uses to decide a state
// file is its own) and both parts are present. Anything else — no ledger, no
// state, an unreadable file, another repo's state — returns owner and name
// unchanged.
func CanonicalRepo(ledgerPath, owner, name string) (string, string) {
	if ledgerPath == "" {
		return owner, name
	}
	st, err := LoadState(ledgerPath)
	if err != nil || st == nil {
		return owner, name
	}
	if !strings.EqualFold(st.Repo, owner+"/"+name) {
		return owner, name
	}
	if st.RepoMeta == nil || st.RepoMeta.Owner == "" || st.RepoMeta.Name == "" {
		return owner, name
	}
	return st.RepoMeta.Owner, st.RepoMeta.Name
}
