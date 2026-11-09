package web

// pageAssets are the style and script files a page loads besides the shared
// ui-*.css and ui-parts.js: the rules and behaviour that belong to that page
// alone live in files named after it (ui-inbox.css, ui-inbox.js). The layout
// writes them after ui-pages.css and after ui-parts.js.
var pageAssets = map[string]struct{ CSS, JS []string }{
	"reposettings": {CSS: []string{"ui-settings.css", "ui-labels.css"}, JS: []string{"ui-settings.js"}},
	"setup":        {CSS: []string{"ui-settings.css"}},
	"orgs":         {CSS: []string{"ui-settings.css"}, JS: []string{"ui-settings.js"}},
	"tokens":       {CSS: []string{"ui-settings.css"}},
	"profile":      {CSS: []string{"ui-settings.css", "ui-agents.css"}},
	"device":       {CSS: []string{"ui-settings.css"}},
	"devicecheck":  {CSS: []string{"ui-settings.css"}},
	"devicedone":   {CSS: []string{"ui-settings.css"}},
	"interactive":  {CSS: []string{"ui-settings.css"}},
	"inbox":        {CSS: []string{"ui-inbox.css"}, JS: []string{"ui-inbox.js"}},
	"issues":       {CSS: []string{"ui-labels.css", "ui-issues.css"}},
	"issue":        {CSS: []string{"ui-labels.css", "ui-issue.css"}},
	"pain":         {CSS: []string{"ui-labels.css", "ui-issues.css", "ui-issue.css"}},
	"idea":         {CSS: []string{"ui-labels.css", "ui-issues.css", "ui-issue.css"}},
	"repocode":     {CSS: []string{"ui-code.css"}},
	"repoblob":     {CSS: []string{"ui-code.css"}},
	"pulls":        {CSS: []string{"ui-issues.css", "ui-pulls.css"}},
	"pull":         {CSS: []string{"ui-pulls.css"}},
	"commits":      {CSS: []string{"ui-pulls.css"}, JS: []string{"ui-pulls.js"}},
	"commit":       {CSS: []string{"ui-pulls.css"}},
	"search":       {CSS: []string{"ui-search.css"}},
	"newissue":     {CSS: []string{"ui-labels.css", "ui-issues.css", "ui-newissue.css"}, JS: []string{"ui-newissue.js"}},
}
