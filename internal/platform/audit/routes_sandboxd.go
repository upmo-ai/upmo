package audit

func sandboxdRoutes() []entry {
	exempt := Route{Exempt: exemptService}
	return []entry{
		{pattern: "POST /upmo.sandboxd.v1.Runtime/Create", route: exempt},
		{pattern: "POST /upmo.sandboxd.v1.Runtime/Exec", route: exempt},
		{pattern: "POST /upmo.sandboxd.v1.Runtime/ReadFile", route: exempt},
		{pattern: "POST /upmo.sandboxd.v1.Runtime/WriteFile", route: exempt},
		{pattern: "POST /upmo.sandboxd.v1.Runtime/Pause", route: exempt},
		{pattern: "POST /upmo.sandboxd.v1.Runtime/Destroy", route: exempt},
		{pattern: "POST /upmo.sandboxd.v1.Runtime/StartEditor", route: exempt},
		{pattern: "POST /upmo.sandboxd.v1.Runtime/StopEditor", route: exempt},
		{pattern: "POST /upmo.sandboxd.v1.Runtime/PTY", route: exempt},
		{pattern: "POST /upmo.sandboxd.v1.Runtime/Expose", route: exempt},
	}
}
