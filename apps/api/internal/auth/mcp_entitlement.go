package auth

// MCPConnectorEntitled reports whether an org on plan may use a cloud
// connector whose minimum plan is minPlan, and the plan to move to when it may
// not. This edition sells no plans: every connector an install offers is its
// own, whatever the minimum written in the catalog.
func MCPConnectorEntitled(plan, minPlan string) (entitled bool, upgradeTo string) {
	return true, ""
}
