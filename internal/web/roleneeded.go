package web

import (
	"errors"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Rechte sichtbar (REQ-704): Was jemand nicht darf, sagt die Oberfläche mit der
// Rolle, die es braucht, und mit den Menschen, die sie vergeben. Die Sätze
// kommen aus store.RoleNeeded, das die Matrix bei einer Ablehnung liefert.

func init() {
	add := func(key, en, de string) {
		messages[key] = en
		messagesDE[key] = de
	}
	add("err.role.need", "This needs the role %[1]s in %[2]s. You are %[3]s there.", "Dafür brauchst du die Rolle %[1]s in %[2]s. Dort bist du %[3]s.")
	add("err.role.need_none", "This needs the role %[1]s in %[2]s. You have no role there.", "Dafür brauchst du die Rolle %[1]s in %[2]s. Dort hast du keine Rolle.")
	add("err.role.ask", "Ask %s for it.", "Frag %s danach.")
	add("err.role.ask_owner", "An owner of the organization can give it to you.", "Ein Owner der Organisation kann sie dir geben.")
	add("err.role.or", "%[1]s or %[2]s", "%[1]s oder %[2]s")
	add("err.role.short", "Needs %[1]s · ask %[2]s", "Ab %[1]s · frag %[2]s")
	add("err.role.short_owner", "Needs %s · an owner gives it", "Ab %s · vergibt ein Owner")
	add("adm.roles_of", "Roles of %s in the projects", "Rollen von %s in den Projekten")
	add("adm.role_change", "Change", "Ändern")
	add("adm.invite.past", "Earlier invitations (%d)", "Frühere Einladungen (%d)")
	add("adm.invite.guest_off_enforce", "Only when the server enforces roles (not with GHOSTTREE_ENFORCE_ACCESS=0)", "Erst wenn der Server Rollen durchsetzt (nicht mit GHOSTTREE_ENFORCE_ACCESS=0)")
	add("adm.invite.guest_off_project", "Needs a project first", "Braucht zuerst ein Projekt")
}

// roleNeededOf is the RoleNeeded inside err, nil for any other error.
func roleNeededOf(err error) *store.RoleNeeded {
	var need *store.RoleNeeded
	if errors.As(err, &need) {
		return need
	}
	return nil
}

// roleWord is a project role as the page names it ("Lead", "Mitglied").
func roleWord(l lang, role string) string { return l.msg("role." + role) }

// orNames joins names as "A, B or C".
func orNames(l lang, names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return l.msg("err.role.or", strings.Join(names[:len(names)-1], ", "), names[len(names)-1])
}

// roleNeededText is the whole sentence of a refusal: the role it needs, the
// viewer's own and whom to ask.
func roleNeededText(l lang, need *store.RoleNeeded) string {
	text := l.msg("err.role.need_none", roleWord(l, need.Need), shortName(need.Project))
	if need.Have != "" {
		text = l.msg("err.role.need", roleWord(l, need.Need), shortName(need.Project), roleWord(l, need.Have))
	}
	if who := need.Grantors(); len(who) > 0 {
		return text + " " + l.msg("err.role.ask", orNames(l, who))
	}
	return text + " " + l.msg("err.role.ask_owner")
}

// roleNeededShort is the line beside a button the viewer may not use:
// "Ab Lead · frag Mara Keller".
func roleNeededShort(l lang, need *store.RoleNeeded) string {
	if need == nil {
		return ""
	}
	if who := need.Grantors(); len(who) > 0 {
		return l.msg("err.role.short", roleWord(l, need.Need), orNames(l, who))
	}
	return l.msg("err.role.short_owner", roleWord(l, need.Need))
}
