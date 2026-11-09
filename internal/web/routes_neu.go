package web

// Die Routen der Seiten für Features, Maschinen und die Werkstatt. Sie kommen beim
// Start in die Routentabelle, damit web.go dafür nichts ändern muss.
func init() {
	for pattern, class := range map[string]webClass{
		"GET /ui/f/{n}":                  webProject,
		"GET /ui/m/{machine}":            webAccount,
		"POST /ui/m/{machine}/share":     webAdmin,
		"POST /ui/m/{machine}/unshare":   webAdmin,
		"POST /ui/m/{machine}/owner":     webAdmin,
		"POST /ui/m/{machine}/entry":     webAdmin,
		"POST /ui/ideas/{id}/phase":      webAdmin,
		"POST /ui/decisions/{id}/vote":   webAdmin,
		"POST /ui/decisions/{id}/close":  webAdmin,
		"POST /ui/decisions/{id}/reopen": webAdmin,
	} {
		webRoutes[pattern] = class
	}
}
