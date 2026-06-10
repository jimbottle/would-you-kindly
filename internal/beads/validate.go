package beads

// ValidIssueTypes is bd's accepted --type vocabulary; bd rejects
// anything else at write time. Kept here, next to the client that
// shells out to bd, so CLI-side validation and the bd contract live
// in one place (SetIssueType and CreateOptions document the same
// list in prose).
var ValidIssueTypes = []string{
	"task", "bug", "feature", "chore", "epic",
	"decision", "spike", "story", "milestone",
}

// IsValidIssueType reports whether t is one of bd's accepted issue
// types. Case-sensitive on purpose: bd is, and silently lowercasing
// here would vouch for an invocation bd then rejects.
func IsValidIssueType(t string) bool {
	for _, v := range ValidIssueTypes {
		if t == v {
			return true
		}
	}
	return false
}

// IsValidPriority reports whether p is one of bd's accepted priority
// spellings: 0-4 or P0-P4 (either case for the P). Validating at
// parse time keeps a typo like "banana" from reaching bd — and, more
// importantly, keeps -dry-run from vouching for an invocation that
// would fail for real (would-you-kindly-ure8).
func IsValidPriority(p string) bool {
	if len(p) == 2 && (p[0] == 'P' || p[0] == 'p') {
		p = p[1:]
	}
	return len(p) == 1 && p[0] >= '0' && p[0] <= '4'
}
