package ws

import "time"

// SetRecheck changes how often sockets re-check access to their joined
// projects; sockets opened afterwards use it.
func (h *Hub) SetRecheck(d time.Duration) { h.recheck = d }
