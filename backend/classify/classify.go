package classify

import "strings"

func Category(email string, hasListUnsubscribe bool, hasPromotions bool, hasSocial bool, hasUpdates bool, hasPersonal bool, hasInbox bool) string {
	lower := strings.ToLower(email)
	isNoReply := strings.Contains(lower, "noreply") ||
		strings.Contains(lower, "no-reply") ||
		strings.Contains(lower, "donotreply") ||
		strings.Contains(lower, "notifications") ||
		strings.Contains(lower, "alerts")

	switch {
	case hasListUnsubscribe:
		return "Newsletter"
	case hasPromotions:
		return "Promotional"
	case hasSocial:
		return "Social"
	case hasUpdates:
		return "Notification"
	case isNoReply:
		return "No-reply"
	case hasPersonal || hasInbox:
		return "Personal"
	default:
		return "Other"
	}
}

func Score(category string, hasInbox bool) int {
	score := 50
	if hasInbox {
		score += 20
	}
	switch category {
	case "Personal":
		score += 30
	case "Newsletter":
		score -= 30
	case "No-reply":
		score -= 20
	case "Promotional":
		score -= 20
	}
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}
