//go:build readmeshots

package main

// extras adds what the README pictures need beyond the promo dataset: file
// descriptions (ghosts) beside the code tree, an idea in the workshop with a
// concept and an open decision, a workflow with a finished run, a feature, a
// runbook, and a work package bound to a pull request. Invented data only.

import (
	"time"

	"fmt"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func demoSHA(n int) string {
	return fmt.Sprintf("%07x%033x", 0x5e1f00d+n*0x2b3d, uint64(n)*0x9e3779b97f4a7c15)
}

func ptr[T any](v T) *T { return &v }

func (s *seeder) extras() {
	st := s.st
	anchor := func(n int, branch string) *gitanchor.Anchor {
		return &gitanchor.Anchor{Commit: demoSHA(n), Branch: branch}
	}

	// ---- ghosts: descriptions beside the code tree of storefront
	ghosts := []struct{ path, kind, text string }{
		{"", "dir", "The Northwind shop: product pages, cart and checkout. Payments live in the payments repository; this one only calls its API."},
		{"checkout", "dir", "Everything between the cart and the paid order. Guest and account checkout share one flow; only the last step differs."},
		{"checkout/guest.go", "file", "Guest checkout: collects an email address instead of an account. The address is only used for the receipt and the tracking emails, never for marketing."},
		{"checkout/receipt.go", "file", "Sends the receipt once, when payments reports the order as paid. A retry of the payment must not send a second one (see the pitfall on duplicate receipts)."},
		{"checkout/shipping.go", "file", "Shipping prices per parcel size. The tiers are a business decision and come from the task Theo answered, not from code."},
		{"cart", "dir", "The cart in the browser and its copy on the server. An empty cart is a valid state."},
		{"cart/cart.go", "file", "Adds, removes and prices items. Prices are in cents; rounding happens once, at the total."},
		{"catalog", "dir", "Product pages and photos. Photos load as you scroll; the page itself stays under 1 MB."},
		{"catalog/images.go", "file", "Builds the responsive image set for a product photo. Widths are fixed so the CDN cache stays small."},
		{"emails", "dir", "Order emails: confirmation, shipped, delivered. Customers can switch the last two off."},
	}
	for _, g := range ghosts {
		_, err := st.PutGhostFile(store.GhostFile{Project: sf, Path: g.path, Kind: g.kind, Description: g.text, Person: "Mia", Harness: "claude",
			GitAnchor: anchor(3, "main")})
		must(err)
	}

	// ---- an idea in the workshop: raw note, concept, open decision
	mia := idea.Actor{AccountID: 1, Person: "Mia"}
	agent := idea.Actor{AccountID: 1, Person: "Mia", Agent: true}
	id, err := s.mia.CreateIdea(store.CreateIdeaInput{Project: sf, Title: "Keep the cart across devices",
		Body: "Shoppers add things on the phone and buy on the laptop. The cart should follow them.", Actor: mia})
	must(err)
	_, _, err = s.mia.PutIdeaText(id.ID, store.PutIdeaTextInput{Part: "raw", Source: "voice note, Monday stand-up", Actor: mia,
		Body: "half the support mails this month are 'my cart is empty on the laptop'. people browse on the train and buy at home. could we just keep the cart for guests too, maybe by email link? not sure about privacy."})
	must(err)
	_, _, err = s.mia.PutIdeaConcept(id.ID, store.PutIdeaConceptInput{Actor: agent, Message: "first concept from the voice note",
		Patch: idea.ConceptPatch{
			About:     ptr("Shoppers browse on the phone and buy on the laptop. Today the cart stays on the device it was filled on, and support gets about forty mails a month about it."),
			What:      ptr("A cart that follows the shopper: signed-in shoppers see the same cart everywhere, guests can carry it over with a link."),
			How:       ptr("Signed-in carts move to the server cart that already exists. For guests a one-time link by email restores the cart on another device."),
			Not:       ptr("No tracking across devices without the shopper asking for it. No merging of two carts with different prices; the newer one wins."),
			Questions: ptr("How long does a guest cart live on the server?"),
			Requirements: &[]string{
				"A signed-in shopper sees the same cart on every device within a minute",
				"A guest can send the cart to their own email and restore it elsewhere",
				"A guest cart on the server is deleted after the decided retention",
			}}})
	must(err)
	_, err = s.mia.SetIdeaPhase(id.ID, "discussion", mia, 0)
	if err != nil {
		_, err = s.mia.StartIdeaDiscussion(id.ID)
	}
	must(err)
	dec, err := s.mia.OpenDecision(store.OpenDecisionInput{Subject: "idea", SubjectID: id.ID, Way: "human",
		Question: "How long does a guest cart live on the server?",
		Detail:   "Longer helps people who come back after the weekend. Shorter keeps less personal data.",
		Variants: []idea.VariantInput{
			{Title: "7 days", Pro: "Covers the weekend gap", Contra: "Holds email addresses for a week"},
			{Title: "30 days", Pro: "Catches most returning shoppers", Contra: "Needs a line in the privacy notice"},
			{Title: "Only until the link is used", Pro: "Almost no data kept", Contra: "A second device cannot restore it again"},
		},
		Actor: agent})
	must(err)
	_, err = s.mia.VoteDecision(dec.ID, "A", "Support mails show most people come back within three days; a week covers that with the least data.", agent)
	must(err)

	// ---- a feature
	fname := "Guest checkout"
	_, err = s.mia.CreateFeature(store.CreateFeatureInput{Project: sf, Message: "first version", Requests: []int64{s.reqID[0]},
		Content: feature.Patch{Name: &fname,
			Summary:  ptr("Shoppers pay without creating an account and get their receipt and tracking emails by address."),
			Behavior: ptr("Checkout asks for an email address instead of a sign-in. The receipt goes out once, when the payment is confirmed."),
			Limits:   ptr("No saved addresses for guests. Refunds for guest orders go through support."),
			Areas:    &[]string{"checkout/", "emails/"}},
		Actor: feature.Actor{AccountID: 1, Person: "Mia"}})
	must(err)

	// ---- a runbook
	_, err = st.WriteRunbook(store.RunbookWrite{Project: sf, Name: "deploy-storefront", Person: "Mia", AuthorID: 1, Approve: true,
		Note: "written after the second manual deploy",
		Spec: runbook.Spec{
			Purpose:       "Deploy the shop to production with a health check and a way back.",
			Preconditions: []string{"CI on main is green", "No migration is pending in payments"},
			Steps: []runbook.Step{
				{Title: "Build the image for the tagged commit", Command: "make image TAG={{tag}}"},
				{Title: "Push it to the registry", Command: "make push TAG={{tag}}"},
				{Title: "Roll out to production", Command: "make rollout ENV=prod TAG={{tag}}"},
				{Title: "Wait until the shop answers", Check: "curl -fsS https://shop.example/healthz"},
			},
			Rollback: []runbook.Step{
				{Title: "Roll back to the previous tag", Command: "make rollout ENV=prod TAG={{previous}}"},
			},
			Params: []runbook.Param{{Name: "tag"}, {Name: "previous"}},
		}})
	must(err)

	// ---- knowledge written at a commit
	_, err = st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "A payment retry must not send a second receipt",
		Body:  "The provider calls the webhook again when it does not get an answer within 5 seconds. checkout/receipt.go sends only when the order changes to paid, never on the webhook itself.",
		Scope: scope.Axes{Project: sf}, Person: "Theo", Confidence: "trusted", GitAnchor: anchor(7, "fix/double-receipt")})
	must(err)

	// ---- the lesson of the duplicate receipts: proposed by the agent that fixed
	// them, waiting for a person, and later followed by the refunds session.
	lesson, err := st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "Refunds send their receipt through the same path as payments",
		Body:  "A refund confirmation can arrive twice, like a payment callback. Send the refund receipt only when the refund changes to done, never on the callback itself. Same cause as the duplicate receipts fixed on fix/double-receipts.",
		Scope: scope.Axes{Project: pay}, Person: "Mia", Confidence: "staged", Origin: "agent", Harness: "claude",
		SessionRef: fmt.Sprintf("session:%d", s.sessID["a5"]), GitAnchor: anchor(9, "fix/double-receipts")})
	must(err)
	must(st.SaveAgentExplanation("knowledge", lesson, "The agent that fixed the duplicate receipts noticed that refunds use the same path. Once you approve it, every later session on payments is warned before it touches refunds."))
	s.later("knowledge", lesson, 40*time.Minute)
	s.say("a3", pay, fmt.Sprintf("Partial refunds work. The refund receipt goes out once, when the refund is marked done; a repeated callback sends nothing. I followed KN-%d.", lesson), "", nil, 0, 4*time.Minute, "")

	// ---- a work package bound to a pull request
	p, known := st.ProjectByRemote(sf)
	if known {
		_, err = st.ConnectSourceWithInstallation(miaID, p.OrgID, "github", "northwind", 4242, nil)
		must(err)
		_, err = st.CreatePackage(requestdomain.PackageInput{Project: sf, Branch: "feat/guest-checkout", Name: "Guest checkout", Person: "Mia",
			Members: []requestdomain.PackageMember{{RequestID: s.reqID[0]}, {RequestID: s.reqID[2], Role: "optional"}}})
		must(err)
		body := "Shoppers can pay without an account. Receipt and tracking emails go to the address they enter."
		_, err = st.RecordPullRequest(store.PullRequestChange{InstallationID: 4242, Remote: sf, HeadRemote: sf, Number: 17, Action: "opened",
			Title: "Guest checkout", Head: "feat/guest-checkout", Base: "main", DefaultBranch: "main", State: "open", Author: "mia-nw",
			HeadSHA: demoSHA(11), Body: &body})
		must(err)
	}

	// ---- a workflow and one finished run
	text := "---\ndescription: Compares a new idea with the open ideas and features and leaves a comment\non: idea.created\nuses: ghosttree/idea-match@v1\nresults:\n  comment: 1\n  relation: 3\nmodel: medium\nbudget:\n  run_usd: 0.20\n  month_usd: 5\n---\n"
	// Events from before the workflow existed start nothing.
	_, err = st.DispatchWorkflowEvents(0)
	must(err)
	_, err = st.SaveWorkflowDef(miaID, store.WorkflowDefSave{Project: sf, Name: "idea-match", Text: text, Note: "from the template", Via: "web",
		Event: store.EventIdeaCreated, Uses: "ghosttree/idea-match@v1", Summary: "Compares a new idea with the open ideas and features and leaves a comment",
		RunUSD: 0.20, MonthUSD: 5})
	must(err)
	_, err = s.mia.CreateIdea(store.CreateIdeaInput{Project: sf, Title: "Wish list for signed-in shoppers",
		Body: "Let people park items for later without putting them in the cart.", Actor: mia})
	must(err)
	_, err = st.DispatchWorkflowEvents(0)
	must(err)
	if run, _, ok, err := st.ClaimWorkflowRun(); err == nil && ok {
		must(st.FinishWorkflowRun(run.ID, store.WorkflowRunOutcome{State: store.RunDone,
			Result:          "1 comment, 1 relation: close to IDEA-1 (cart across devices), both need a server-side list per shopper",
			WorkflowVersion: "v1", Connection: "team-anthropic", Model: "claude-haiku-5-5", CostUSD: 0.031,
			Inputs: "IDEA-2 Wish list for signed-in shoppers; open ideas: IDEA-1; features: FEAT-1",
			Output: "IDEA-2 and IDEA-1 both need a list of items per shopper on the server. Suggest building the storage once."}))
	} else {
		must(err)
	}
	// The model alias the workflow names, on a demo connection without a key.
	s.postSQL = append(s.postSQL,
		`INSERT INTO ai_connections(org_id,name,format,base_url,key_last4,created_by,created_at) SELECT org_id,'team-anthropic','anthropic','https://api.anthropic.com','x9Qa',1,datetime('now') FROM projects WHERE remote='`+sf+`'`,
		`INSERT INTO ai_aliases(level,org_id,alias,connection_id,model,updated_by,updated_at) SELECT 'owner',org_id,'medium',(SELECT id FROM ai_connections WHERE name='team-anthropic'),'claude-haiku-5-5',1,datetime('now') FROM projects WHERE remote='`+sf+`'`)
}
