package web

import (
	"net/http"

	"github.com/Deadweight-Labs/ghosttree/internal/redact"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// secretRefused answers a form whose text the store refused because it carries a
// secret (PAIN-17), and says whether it did. The person gets one sentence, in
// their language, back on the page of the form, and every other field they typed
// comes back with it. The fields that hold the secret do not: a refused text is
// never written onto a page again, which is the rule for a task note too.
//
// The fields are found by looking at the form, with the same scan the store
// used, because a form field has not always the name the store gives the text
// (the description of a criterion is "description" in one and "text" in another).
func (a *app) secretRefused(w http.ResponseWriter, r *http.Request, err error, back string) bool {
	if requestdomain.Code(err) != "secret_detected" {
		return false
	}
	var drop []string
	for name, values := range r.PostForm {
		for _, v := range values {
			if len(redact.FindSecrets(v)) > 0 {
				drop = append(drop, name)
				break
			}
		}
	}
	a.failFormKeeping(w, r, back, http.StatusUnprocessableEntity, a.langFor(r).msg("err.write_secret"), "", drop...)
	return true
}
