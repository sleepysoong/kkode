package gateway

import "strings"

func runEventsURL(runID string) string {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return ""
	}
	return "/api/v1/runs/" + runID + "/events"
}
