package campaign

// NodeServesCampaign reports whether a node may be assigned a campaign task.
// Standalone tasks (empty campaignID) are always allowed.
// Empty enabled list means the node opts into no campaigns (prod default).
func NodeServesCampaign(enabled []string, campaignID string) bool {
	if campaignID == "" {
		return true
	}
	if len(enabled) == 0 {
		return false
	}
	for _, id := range enabled {
		if id == campaignID {
			return true
		}
	}
	return false
}
