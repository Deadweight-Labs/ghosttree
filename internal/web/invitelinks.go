package web

import (
	"sync"
	"time"
)

// maxInviteLinks bounds the memory held for links: the oldest go first.
const maxInviteLinks = 500

// maxInviteLinksPerAccount keeps one creator from pushing everybody else's
// links out of memory: past it, that creator's own oldest link goes first.
const maxInviteLinksPerAccount = 50

type inviteLink struct {
	account string
	code    string
	project bool // the code belongs to a project invitation (/join/<code>)
	until   time.Time
}

// inviteLinks keeps the links of open invitations for the account that
// created them, in memory only. The store holds nothing but the hash of a code,
// so without this the link would be lost the moment the page after "Create"
// reloads. A link is shown to its creator alone, never persisted, and dropped
// when the invitation ends (revoked, used) or expires; a restart forgets all of
// them and the owner makes a new one.
type inviteLinks struct {
	mu    sync.Mutex
	now   func() time.Time
	links map[int64]inviteLink
	order []int64
}

func newInviteLinks() *inviteLinks {
	return &inviteLinks{now: time.Now, links: map[int64]inviteLink{}}
}

func (l *inviteLinks) put(id int64, account, code string, project bool, until time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.links[id]; !ok {
		l.order = append(l.order, id)
	}
	l.links[id] = inviteLink{account: account, code: code, project: project, until: until}
	for l.countLocked(account) > maxInviteLinksPerAccount {
		for _, old := range l.order {
			if l.links[old].account == account {
				l.removeLocked(old)
				break
			}
		}
	}
	for len(l.order) > maxInviteLinks {
		delete(l.links, l.order[0])
		l.order = l.order[1:]
	}
}

// get returns the link of invitation id for its creator only.
func (l *inviteLinks) get(id int64, account string) (inviteLink, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	link, ok := l.links[id]
	if !ok || link.account != account {
		return inviteLink{}, false
	}
	if !l.now().Before(link.until) {
		l.removeLocked(id)
		return inviteLink{}, false
	}
	return link, true
}

func (l *inviteLinks) drop(id int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.removeLocked(id)
}

func (l *inviteLinks) countLocked(account string) int {
	n := 0
	for _, link := range l.links {
		if link.account == account {
			n++
		}
	}
	return n
}

func (l *inviteLinks) removeLocked(id int64) {
	delete(l.links, id)
	for i, v := range l.order {
		if v == id {
			l.order = append(l.order[:i], l.order[i+1:]...)
			break
		}
	}
}

func (l *inviteLinks) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.links)
}
