package claims

// Pending is a device an attach may have left behind. Its record is written before the device
// is created and removed only once the device's absence is verified (ADR-0007).
type Pending struct {
	Claim
	// Token is the device's alias, set after creation. A device without it is not ours. Random per
	// attach, 32 bits: a collision would let a retire drop the wrong record.
	Token string `json:"token"`
	// Temp is the host-side name the device was created under, before it moved into the netns.
	Temp string `json:"temp"`
}

// AddPending returns s with rec recorded.
func (s State) AddPending(rec Pending) State {
	out := s.clone()
	out.Pending = append(out.Pending, rec)
	return out
}

// RemovePending returns s without every record of network in netns. Use it only for the one
// attach that holds the netns: a retire may still hold a record of the pair, so RemoveToken.
func (s State) RemovePending(netns uint64, network string) State {
	out := s.clone()
	out.Pending = out.Pending[:0]
	for _, p := range s.Pending {
		if p.Netns != netns || p.Network != network {
			out.Pending = append(out.Pending, p)
		}
	}
	return out
}

// RemoveToken returns s without the record with token. A retire drops only the record it verified,
// so a newer record for the same netns and network survives it.
func (s State) RemoveToken(token string) State {
	out := s.clone()
	out.Pending = out.Pending[:0]
	for _, p := range s.Pending {
		if p.Token != token {
			out.Pending = append(out.Pending, p)
		}
	}
	return out
}

// HasPending reports whether a record of network in netns is in s.
func (s State) HasPending(netns uint64, network string) bool {
	for _, p := range s.Pending {
		if p.Netns == netns && p.Network == network {
			return true
		}
	}
	return false
}

// clone copies s, so a change to the result leaves s as it was.
func (s State) clone() State {
	return State{
		Claims:  append([]Claim(nil), s.Claims...),
		Pending: append([]Pending(nil), s.Pending...),
	}
}
