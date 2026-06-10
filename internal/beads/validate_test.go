package beads

import "testing"

func TestIsValidPriority(t *testing.T) {
	valid := []string{"0", "1", "4", "P0", "P4", "p2"}
	for _, p := range valid {
		if !IsValidPriority(p) {
			t.Errorf("IsValidPriority(%q) = false, want true", p)
		}
	}
	invalid := []string{"", "5", "P5", "banana", "10", "Pp4", "-1", "P", "p", "04"}
	for _, p := range invalid {
		if IsValidPriority(p) {
			t.Errorf("IsValidPriority(%q) = true, want false", p)
		}
	}
}

func TestIsValidIssueType(t *testing.T) {
	for _, v := range ValidIssueTypes {
		if !IsValidIssueType(v) {
			t.Errorf("IsValidIssueType(%q) = false for a listed type", v)
		}
	}
	// Case-sensitive on purpose — bd is, and vouching for "Bug" here
	// would just move the failure to write time.
	for _, v := range []string{"", "Bug", "TASK", "ticket", "feat"} {
		if IsValidIssueType(v) {
			t.Errorf("IsValidIssueType(%q) = true, want false", v)
		}
	}
}
