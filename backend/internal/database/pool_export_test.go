package database

// NewAppRolePool exposes the pool construction NewPool and NewAdminPool
// share to the external tests: TestAppPoolDisablesJIT checks the pool of the
// command-line tools through it, as NewAdminPool itself may not be referenced
// under internal/ (TestAdminPoolCallersAreListed).
var NewAppRolePool = newAppRolePool
