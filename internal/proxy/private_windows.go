package proxy

import "github.com/nearhost/nearhost/internal/winsys"

// restrictStorage gives the storage directory an access list only the
// user, SYSTEM and Administrators are on. A Windows folder takes its
// parent's list, which is private under %LOCALAPPDATA% but need not be
// where XDG_DATA_HOME or a redirected profile puts it. Tests replace it.
var restrictStorage = winsys.RestrictToOwner
