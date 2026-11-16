package audit

func provisionRoutes() []entry {
	return []entry{
		{pattern: "POST /api/v1/provision/members", route: Route{Action: ProvisionMemberCreate}},
		{pattern: "PUT /api/v1/provision/members/{external_id}", route: Route{Action: ProvisionMemberUpdate}},
		{pattern: "DELETE /api/v1/provision/members/{external_id}", route: Route{Action: ProvisionMemberDelete}},
		{pattern: "POST /api/v1/provision/teams", route: Route{Action: ProvisionTeamCreate}},
		{pattern: "PUT /api/v1/provision/teams/{external_id}", route: Route{Action: ProvisionTeamUpdate}},
		{pattern: "DELETE /api/v1/provision/teams/{external_id}", route: Route{Action: ProvisionTeamDelete}},
		{pattern: "POST /api/v1/provision/sign-in-links", route: Route{Action: ProvisionSignInLinkIssue}},
		{pattern: "PUT /api/v1/provision/providers", route: Route{Action: ProvisionProviderUpsert}},
		{pattern: "PUT /api/v1/provision/quotas", route: Route{Action: ProvisionQuotaSet}},
		{pattern: "DELETE /api/v1/provision/quotas", route: Route{Action: ProvisionQuotaClear}},
		{pattern: "PUT /api/v1/provision/packs", route: Route{Action: ProvisionPackInstall}},
	}
}
