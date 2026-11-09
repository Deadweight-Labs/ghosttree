package store

// everydayWords are words of everyday German and English that are never the
// subject of a sentence: auxiliaries and modals in their inflected forms,
// question words, pronouns, conjunctions, filler adverbs and the commonest
// verbs. The frequency gates cannot catch them in this archive: its knowledge is
// technical and largely English, so "wohin" or "hattest" stand in no entry at
// all and looked exactly like a codename that never became one, and a word a
// person says now and then is not frequent in their own session text either.
//
// The list is a floor under the gates, not a replacement for them. It holds
// only words that no project would name a thing after; anything else is still
// judged by the gates and, for a hint, by how many sessions and requests name
// it (see the server's hint).
var everydayWords = wordSet(
	// German: auxiliaries and modals.
	"bin", "bist", "seid", "waren", "warst", "wart", "wäre", "wärst", "wären", "wärt", "gewesen",
	"hast", "habt", "hatte", "hattest", "hatten", "hattet", "hätte", "hättest", "hätten", "hättet", "gehabt",
	"werde", "wirst", "werdet", "wurde", "wurdest", "wurden", "wurdet", "würde", "würdest", "würden", "würdet", "geworden",
	"kannst", "können", "könnt", "konnte", "konntest", "konnten", "könnte", "könntest", "könnten", "gekonnt",
	"musst", "müsst", "musste", "musstest", "mussten", "müsste", "müsstest", "müssten",
	"sollst", "sollt", "solltest", "sollten", "will", "willst", "wollt", "wollte", "wolltest", "wollten",
	"darf", "darfst", "dürfen", "dürft", "durfte", "durften", "dürfte", "dürften",
	"mag", "magst", "mögen", "mochte", "möchte", "möchtest", "möchten",
	// German: question words.
	"wer", "wen", "wem", "wessen", "wann", "woher", "wohin", "warum", "wieso", "weshalb", "weswegen",
	"welche", "welcher", "welches", "welchen", "welchem", "womit", "wofür", "worüber", "woran", "worauf",
	"wozu", "wodurch", "wovon", "worum", "wobei",
	// German: pronouns and articles.
	"du", "dich", "dir", "ihn", "ihm", "ihr", "ihre", "ihren", "ihrem", "ihrer", "ihnen", "euch", "euer", "eure",
	"mein", "meine", "meinen", "meinem", "meiner", "dein", "deine", "deinen", "deinem", "deiner",
	"seine", "seinen", "seinem", "seiner", "unser", "unsere", "unseren", "unserem", "unserer",
	"einem", "eines", "kein", "keine", "keinen", "keinem", "keiner", "dies", "diese", "dieser", "dieses",
	"diesen", "diesem", "jene", "jener", "jedes", "jeder", "jede", "jeden", "selbst", "selber",
	// German: conjunctions and prepositions.
	"denn", "sondern", "weil", "obwohl", "damit", "sodass", "falls", "bevor", "nachdem", "während",
	"seit", "sobald", "solange", "ohne", "gegen", "unter", "zwischen", "durch", "wegen", "trotz", "statt",
	"anstatt", "neben", "hinter", "vor", "ins", "im", "am", "beim", "zum", "vom",
	// German: adverbs and fillers.
	"sehr", "weniger", "gar", "halt", "eben", "eigentlich", "gerade", "grad", "jetzt", "dann", "danach",
	"davor", "dabei", "dafür", "dagegen", "daher", "darum", "deshalb", "deswegen", "trotzdem", "immer",
	"nie", "niemals", "nichts", "alles", "viel", "viele", "vielen", "wenig", "wenige", "hier", "heute",
	"gestern", "morgen", "bald", "gleich", "sofort", "vielleicht", "wohl", "nein", "einmal", "nochmal",
	"wieder", "weiter", "genau", "richtig", "falsch", "gut", "schlecht", "klar", "sicher", "danke",
	"okay", "sonst", "überhaupt", "irgendwie", "irgendwo", "irgendwas", "sowieso", "vorher", "nachher",
	"oben", "unten", "drin", "draußen", "eher", "lieber", "fast", "kaum", "erst", "zuerst", "zuletzt",
	"schließlich", "endlich", "natürlich", "wirklich", "ziemlich", "etwa", "bereits", "zusammen",
	"allein", "jemand", "niemand", "manche", "mancher", "beide", "beiden", "andere", "anderen", "anders",
	"neu", "neue", "neuen", "alt", "alte", "alten", "ok", "naja", "hmm", "ach", "oh",
	// German: the commonest verbs and their forms.
	"tu", "tut", "getan", "geh", "ging", "gegangen", "kommen", "komm", "kommt", "kam", "gekommen",
	"sehen", "sieh", "sieht", "sah", "gesehen", "sagen", "sag", "sagt", "sagte", "gesagt",
	"geben", "gib", "gab", "gegeben", "nehmen", "nimm", "nimmt", "nahm", "genommen",
	"finden", "find", "findet", "fand", "gefunden", "lassen", "lass", "lässt", "ließ", "gelassen",
	"bleiben", "bleib", "bleibt", "blieb", "geblieben", "stehen", "stand", "liegen", "liegt", "lag",
	"zeigen", "zeig", "zeigt", "zeigte", "gezeigt", "braucht", "brauchte", "gebraucht",
	"wissen", "weiß", "weißt", "wusste", "gewusst", "glauben", "glaube", "glaubst", "denke", "denkst",
	"denkt", "dachte", "meinst", "meint", "meinte", "fragen", "frag", "fragt", "fragte", "gefragt",
	"schauen", "schau", "schaut", "geschaut", "trag", "trägt", "tragen", "getragen", "versuch",
	"versuche", "versuchen", "versucht", "kriegen", "kriegt", "bekommen", "bekommt", "hol", "holen",
	"holt", "geholt", "setz", "setzen", "setzt", "gesetzt", "stell", "stellen", "stellt", "gestellt",
	"schreib", "schreiben", "schreibt", "geschrieben", "lies", "lesen", "liest", "gelesen",
	"leg", "legen", "legt", "gelegt", "mache", "machst", "machte", "tust", "gehst", "gehe",
	"funktioniert", "klappt", "passt", "fehlt", "läuft", "gibts",
	// English: auxiliaries and modals.
	"was", "been", "being", "did", "done", "doing", "had", "having", "shall", "could", "might", "may",
	"isn", "aren", "wasn", "weren", "don", "doesn", "didn", "won", "wouldn", "couldn", "shouldn",
	"haven", "hasn", "hadn",
	// English: question words, pronouns, conjunctions.
	"who", "whom", "whose", "where", "why", "whether", "whatever", "him", "his", "her", "hers", "she",
	"our", "ours", "yours", "mine", "myself", "yourself", "itself", "those", "each", "every", "other",
	"because", "although", "though", "unless", "until", "while", "since", "before", "over", "under",
	"between", "through", "without", "within", "upon", "onto", "off",
	// English: adverbs and fillers.
	"also", "again", "still", "already", "yet", "very", "really", "maybe", "perhaps", "here", "today",
	"tomorrow", "yesterday", "soon", "always", "never", "ever", "often", "sometimes", "much", "many",
	"most", "less", "least", "few", "enough", "quite", "rather", "almost", "anyway", "actually",
	"basically", "probably", "sure", "okay", "yes", "thanks", "thank", "well", "right", "wrong",
	"good", "bad", "new", "old", "same", "different", "something", "nothing", "everything",
	"anything", "someone", "anyone", "everyone", "somewhere", "anywhere", "everywhere",
	// English: the commonest verbs.
	"go", "goes", "went", "gone", "come", "comes", "came", "see", "sees", "saw", "seen", "say", "says",
	"said", "give", "gives", "gave", "given", "take", "takes", "took", "taken", "find", "finds",
	"found", "let", "lets", "keep", "keeps", "kept", "show", "shows", "showed", "shown", "know",
	"knows", "knew", "known", "think", "thinks", "thought", "mean", "means", "meant", "ask", "asks",
	"asked", "look", "looks", "looked", "try", "tries", "tried", "use", "uses", "used", "work",
	"works", "worked", "got", "made", "makes", "put", "puts", "tell", "tells", "told",
)

func wordSet(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}
