package security

// WithFindingsForTest replaces the finding repository of the match (a
// repository that changes a finding between the read and the resolution).
func (m *Matcher) WithFindingsForTest(r Repository) *Matcher {
	m.findings = r
	return m
}
