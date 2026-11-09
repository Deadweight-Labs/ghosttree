package web

// Texte der letzten Seiten von REQ-599 (Teil 6): Kommentare an Issue, Idee und
// Pain, Herkunft beim Anlegen, Posteingang mit Erledigten, Hover-Karten und die
// Seiten, die von der alten Oberfläche übrig waren. Eigene Datei, damit sie sich
// nicht mit den anderen Katalogen ins Gehege kommt; der Katalog nimmt sie beim
// Start auf.

var messagesC = map[string]string{
	// Kommentare
	"comments.commented":       "commented %s",
	"comments.edit":            "Edit comment",
	"comments.delete":          "Delete comment",
	"comments.edited":          "edited",
	"comments.edited_title":    "Edited %s",
	"comments.history":         "Earlier versions",
	"comments.version":         "Version %d",
	"comments.deleted":         "This comment was deleted.",
	"comments.deleted_by":      "Deleted by %s",
	"comments.update":          "Update comment",
	"comments.cancel":          "Cancel",
	"comments.cx.comment":      "Comment",
	"comments.cx.submit":       "Comment",
	"comments.cx.placeholder":  "Write a comment",
	"comments.result.added":    "Comment added.",
	"comments.result.edited":   "Comment updated.",
	"comments.result.deleted":  "Comment deleted.",
	"comments.result.empty":    "A comment needs text.",
	"comments.result.too_long": "That comment is too long. A comment may have up to %s characters.",
	"comments.result.failed":   "The comment could not be saved.",
	"issue.cx.drop_text":       "Drop with comment",
	"ideas.cx.dismiss_text":    "Dismiss with comment",
	"ideas.cx.reopen_text":     "Reopen with comment",
	"pains.cx.set_text":        "Set to %s with comment",
	"issue.cx.complete_ph":     "Comment (optional)",
	"ideas.cx.comment_ph":      "Write a comment",
	"pains.cx.comment_ph":      "Write a comment",
	"issue.ev.rel.derived":     "marked %s as the origin",
	"requests.rel.derived":     "Derived from",
	// Herkunft beim Anlegen
	"newissue.origin":             "Origin",
	"newissue.parent":             "Parent issue",
	"newissue.derived_from":       "Derived from",
	"newissue.derived_ph":         "IDEA-3 PAIN-7",
	"newissue.err.origin_missing": "A parent or origin does not exist in this repository.",
	"newissue.err.origin_invalid": "Write the parent as REQ-12 and each origin as REQ-12, IDEA-3, PAIN-7 or thread:5.",
	"newissue.err.origin_many":    "That are too many origins.",
	// Posteingang: Erledigte
	"inbox.show_done":     "Show done",
	"inbox.done":          "Done",
	"inbox.done_none":     "Nothing was finished in the last 30 days.",
	"inbox.how.done":      "ticked off",
	"inbox.how.confirmed": "confirmed",
	"inbox.how.dropped":   "dropped",
	"inbox.how.decided":   "decided",
	"inbox.how.deferred":  "put off",
	"inbox.how.resolved":  "answered",
	"inbox.how.dismissed": "dismissed",
	"inbox.how.expired":   "expired",
}

var messagesCDE = map[string]string{
	// Kommentare
	"comments.commented":       "kommentierte %s",
	"comments.edit":            "Kommentar bearbeiten",
	"comments.delete":          "Kommentar löschen",
	"comments.edited":          "bearbeitet",
	"comments.edited_title":    "Bearbeitet %s",
	"comments.history":         "Frühere Fassungen",
	"comments.version":         "Fassung %d",
	"comments.deleted":         "Dieser Kommentar wurde gelöscht.",
	"comments.deleted_by":      "Gelöscht von %s",
	"comments.update":          "Kommentar aktualisieren",
	"comments.cancel":          "Abbrechen",
	"comments.cx.comment":      "Kommentieren",
	"comments.cx.submit":       "Kommentieren",
	"comments.cx.placeholder":  "Kommentar schreiben",
	"comments.result.added":    "Kommentar hinzugefügt.",
	"comments.result.edited":   "Kommentar aktualisiert.",
	"comments.result.deleted":  "Kommentar gelöscht.",
	"comments.result.empty":    "Ein Kommentar braucht Text.",
	"comments.result.too_long": "Der Kommentar ist zu lang. Erlaubt sind höchstens %s Zeichen.",
	"comments.result.failed":   "Der Kommentar ließ sich nicht speichern.",
	"issue.cx.drop_text":       "Mit Kommentar verwerfen",
	"ideas.cx.dismiss_text":    "Mit Kommentar verwerfen",
	"ideas.cx.reopen_text":     "Mit Kommentar wieder öffnen",
	"pains.cx.set_text":        "Mit Kommentar auf %s setzen",
	"issue.cx.complete_ph":     "Kommentar (optional)",
	"ideas.cx.comment_ph":      "Kommentar schreiben",
	"pains.cx.comment_ph":      "Kommentar schreiben",
	"issue.ev.rel.derived":     "hat %s als Ursprung gesetzt",
	"requests.rel.derived":     "Entstanden aus",
	// Herkunft beim Anlegen
	"newissue.origin":             "Herkunft",
	"newissue.parent":             "Eltern-Issue",
	"newissue.derived_from":       "Entstanden aus",
	"newissue.derived_ph":         "IDEA-3 PAIN-7",
	"newissue.err.origin_missing": "Ein Eltern-Issue oder eine Herkunft gibt es in diesem Repo nicht.",
	"newissue.err.origin_invalid": "Das Eltern-Issue als REQ-12 schreiben, jede Herkunft als REQ-12, IDEA-3, PAIN-7 oder thread:5.",
	"newissue.err.origin_many":    "Das sind zu viele Herkünfte.",
	// Posteingang: Erledigte
	"inbox.show_done":     "Erledigte zeigen",
	"inbox.done":          "Erledigt",
	"inbox.done_none":     "In den letzten 30 Tagen wurde nichts erledigt.",
	"inbox.how.done":      "abgehakt",
	"inbox.how.confirmed": "bestätigt",
	"inbox.how.dropped":   "verworfen",
	"inbox.how.decided":   "entschieden",
	"inbox.how.deferred":  "vertagt",
	"inbox.how.resolved":  "beantwortet",
	"inbox.how.dismissed": "abgelehnt",
	"inbox.how.expired":   "abgelaufen",
}

func init() {
	for k, v := range messagesC {
		messages[k] = v
	}
	for k, v := range messagesCDE {
		messagesDE[k] = v
	}
}
