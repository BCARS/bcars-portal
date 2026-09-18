package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Banner messages after a redirect (bcars-portal-9lx).
//
// A POST that succeeds redirects, and the page it lands on says what happened.
// The sentence used to travel in the query string -- `?success=...`, `?flash=...`,
// `?error=...` -- and the page printed whatever arrived. HTML was escaped, so
// this was never XSS; it was worse in one specific way. Anyone could write a
// link that made the portal say a sentence of their choosing, in the portal's
// own chrome, to an officer holding member.update and membership.approve:
//
//	/admin/members/10?flash=Your+account+was+deleted.+Call+814-555-0000+to+restore
//
// A club mailing list is exactly the channel such a link travels on.
//
// So the URL now carries a KEY, and the sentence is looked up here. A key that
// is not in this file produces no banner at all. The only free values a URL can
// contribute are small non-negative integers, and they are re-rendered by the
// entry's own code rather than pasted into it.
//
// Adding a message means adding it here. That is the point: the set of things
// the portal can be made to say is this file, and it is reviewable in one read.

const (
	// flashParam carries the message key. It replaces the old `success`,
	// `flash` and `error` parameters, which differed for no reason -- whether
	// a message is good news is a property of the message, not of the link.
	flashParam = "msg"
	// numParam carries counts, for the handful of messages that report one.
	numParam = "n"
	// whyParam carries reason keys, for a partial result that has to say what
	// stopped the rest.
	whyParam = "why"

	maxFlashNums    = 4
	maxFlashReasons = 6
	maxFlashNum     = 99999
)

type flashKind uint8

const (
	flashSuccess flashKind = iota
	flashFailure
)

// flashEntry is one thing the portal can say. Exactly one of text and build is
// set; build is for the entries that report a count.
type flashEntry struct {
	kind  flashKind
	text  string
	build func(n []int) string
}

// flashCatalog is the complete set of banner messages.
var flashCatalog = map[string]flashEntry{
	// --- Member record ---
	"member.created":      {kind: flashSuccess, text: "Member created"},
	"member.updated":      {kind: flashSuccess, text: "Member updated"},
	"member.deactivated":  {kind: flashSuccess, text: "Member deactivated"},
	"member.reactivated":  {kind: flashSuccess, text: "Member reactivated"},
	"membership.approved": {kind: flashSuccess, text: "Membership approved"},
	"membership.rejected": {kind: flashSuccess, text: "Membership rejected"},
	"note.added":          {kind: flashSuccess, text: "Note added"},
	"contact.added":       {kind: flashSuccess, text: "Contact added"},
	"address.added":       {kind: flashSuccess, text: "Address added"},

	// --- Member access ---
	"access.granted": {kind: flashSuccess, text: "Access granted"},
	"access.revoked": {kind: flashSuccess, text: "Access revoked. It ends on their next page load, including a session already open"},
	"access.created": {kind: flashSuccess, text: "Account created. It has no password and no access until you grant one"},
	"access.reused":  {kind: flashSuccess, text: "That address already had an account, so it was reused"},
	"access.sent":    {kind: flashSuccess, text: "If that address has an account, a sign-in link is on its way"},

	"access.email_required":   {kind: flashFailure, text: "Give a usable email address"},
	"access.unknown_user":     {kind: flashFailure, text: "No account for that address"},
	"access.unknown_person":   {kind: flashFailure, text: "No such member record"},
	"access.already_granted":  {kind: flashFailure, text: "That account already reaches this record"},
	"access.grant_not_found":  {kind: flashFailure, text: "That account does not currently reach this record"},
	"access.kind_required":    {kind: flashFailure, text: "Choose whether this is the member's own record or a delegate"},
	"access.stale":            {kind: flashFailure, text: "Another officer changed this while you were reading it. Reload and try again"},
	"access.failed":           {kind: flashFailure, text: "That change could not be saved. Please try again"},
	"access.need_email":       {kind: flashFailure, text: "Give the email address for the account"},
	"access.need_grant_email": {kind: flashFailure, text: "Give the email address of the account to grant"},
	"access.need_send_email":  {kind: flashFailure, text: "Give the email address to send to"},
	"access.need_account":     {kind: flashFailure, text: "Choose which account to revoke"},
	"access.no_account":       {kind: flashFailure, text: "No account for that address. Create the account first"},
	"access.rate_limited":     {kind: flashFailure, text: "Too many recent requests for that address. Wait a few minutes and try again"},

	// --- Imports ---
	"import.decided":   {kind: flashSuccess, text: "Decision recorded"},
	"import.discarded": {kind: flashSuccess, text: "Import discarded"},
	"import.committed": {kind: flashSuccess, build: func(n []int) string {
		return "Import committed: " + count(n, 0) + " created, " + count(n, 1) + " updated, " + count(n, 2) + " skipped"
	}},
	"import.no_file":    {kind: flashFailure, text: "No file selected"},
	"import.bad_form":   {kind: flashFailure, text: "File too large or invalid form"},
	"import.stale":      {kind: flashFailure, text: "This record was modified by another user. Please reload and try again."},
	"import.failed":     {kind: flashFailure, text: "That import step could not be completed. Please reload and try again"},
	"import.bad_state":  {kind: flashFailure, text: "This import is not in a state that allows that. Reload and look again"},
	"import.unresolved": {kind: flashFailure, text: "Some rows still need a decision before this import can be committed"},

	// --- Change requests, officer side ---
	"request.linked":        {kind: flashSuccess, text: "Linked to the member record"},
	"request.replayed":      {kind: flashSuccess, text: "That item was already decided that way"},
	"request.applied":       {kind: flashSuccess, text: "Approved and applied to the member record"},
	"request.approved":      {kind: flashSuccess, text: "Approved. Apply the change with the usual workflow"},
	"request.rejected":      {kind: flashSuccess, text: "Rejected"},
	"request.held":          {kind: flashSuccess, text: "Held for verification"},
	"request.apply_none":    {kind: flashSuccess, text: "Nothing was applied."},
	"request.applied_count": {kind: flashSuccess, build: func(n []int) string { return "Applied " + itemCount(num(n, 0)) + "." }},
	"request.declined":      {kind: flashSuccess, build: func(n []int) string { return "Declined " + itemCount(num(n, 0)) + "." }},
	// The same sentence as request.declined, shown as a failure because some
	// of what was asked for did not happen; the reasons ride in `why`.
	"request.declined_partial": {kind: flashFailure, build: func(n []int) string { return "Declined " + itemCount(num(n, 0)) + "." }},
	"request.applied_partial":  {kind: flashFailure, build: func(n []int) string { return "Applied " + itemCount(num(n, 0)) + "." }},

	"request.need_target":       {kind: flashFailure, text: "Give the member record this request concerns"},
	"request.unknown_target":    {kind: flashFailure, text: "No member record with that number"},
	"request.closed":            {kind: flashFailure, text: "This request is already closed"},
	"request.was_closed":        {kind: flashFailure, text: "This was already closed"},
	"request.link_failed":       {kind: flashFailure, text: "That link could not be saved. Please try again"},
	"request.decision_failed":   {kind: flashFailure, text: "That decision could not be saved. Please try again"},
	"request.save_failed":       {kind: flashFailure, text: "That could not be saved. Please try again"},
	"request.need_decision":     {kind: flashFailure, text: "Choose approve, reject, or needs verification"},
	"request.need_reason":       {kind: flashFailure, text: "Give a reason, so the member knows why"},
	"request.need_reject":       {kind: flashFailure, text: "Give a reason for the rejection"},
	"request.need_tick":         {kind: flashFailure, text: "Tick the changes you want to apply"},
	"request.need_link":         {kind: flashFailure, text: "Link this request to a member record before approving it"},
	"request.need_note":         {kind: flashFailure, text: "Say how you verified this before approving it"},
	"request.self_review":       {kind: flashFailure, text: "You submitted this request, so another officer must approve this item"},
	"request.item_decided":      {kind: flashFailure, text: "Another officer has already decided this item. Reload to see their decision"},
	"request.stale":             {kind: flashFailure, text: "Another officer changed this request while you were reading it. Reload and try again"},
	"request.done_stale":        {kind: flashFailure, text: "Another officer changed this while you were reading it. Reload and look again"},
	"request.stale_record":      {kind: flashFailure, text: "The record changed while you were reading it, so nothing was applied. Reload and try again"},
	"request.bad_value":         {kind: flashFailure, text: "The suggested value is not valid for this kind of change"},
	"request.no_adapter":        {kind: flashFailure, text: "This suggestion cannot be applied automatically. Use the usual workflow, then reject or hold it here"},
	"request.decide_first":      {kind: flashFailure, text: "Apply or decline the proposed changes first, so the member gets an answer about them"},
	"request.withdraw_too_late": {kind: flashFailure, text: "An officer has already started reviewing this, so it can no longer be withdrawn"},

	// --- Change requests, member side ---
	"suggestion.sent":      {kind: flashSuccess, text: "Your suggestion has been sent to the officers"},
	"suggestion.note_sent": {kind: flashSuccess, text: "Your note has been sent to the officers"},
	"suggestion.withdrawn": {kind: flashSuccess, text: "Your suggestion has been withdrawn"},

	// --- Shared ---
	"form.invalid": {kind: flashFailure, text: "Please check your entries and try again"},
}

// flashReasons are the clauses a partial result appends to say what stopped the
// rest. They are a separate set because they are fragments, not banners: they
// never appear on their own.
var flashReasons = map[string]string{
	"stale":     "One change was left alone because the record moved while you were reading it; reload and look again.",
	"decided":   "One change had already been decided by another officer.",
	"self":      "One change needs a different officer, because you submitted it.",
	"note":      "One change is sensitive and needs a note saying how you verified it.",
	"target":    "One change names no record yet; link this request first.",
	"value":     "One value was not valid for the kind of detail it corrects.",
	"noadapter": "One change cannot be applied here; do it on the record and decline this.",
	"other":     "One change could not be applied.",
}

// flashTarget appends a message key to target, with any counts the message
// reports. Counts are the only free values a banner URL carries.
func flashTarget(target, key string, nums ...int) string {
	return flashTargetWhy(target, key, nums, nil)
}

// flashTargetWhy is flashTarget for a partial result, which also names the
// reasons the rest did not happen.
func flashTargetWhy(target, key string, nums []int, why []string) string {
	q := url.Values{}
	q.Set(flashParam, key)
	for i, n := range nums {
		if i >= maxFlashNums {
			break
		}
		q.Add(numParam, strconv.Itoa(clampFlashNum(n)))
	}
	for i, w := range why {
		if i >= maxFlashReasons {
			break
		}
		q.Add(whyParam, w)
	}

	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	return target + sep + q.Encode()
}

// flashBanner reads the request's message key and returns the sentences to
// show. Both are empty for a key this file does not define, which is what makes
// a crafted link produce nothing rather than a message of its author's choosing.
func flashBanner(r *http.Request) (success, failure string) {
	q := r.URL.Query()
	entry, ok := flashCatalog[q.Get(flashParam)]
	if !ok {
		return "", ""
	}

	var nums []int
	for _, raw := range q[numParam] {
		if len(nums) >= maxFlashNums {
			break
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			// A count that is not a count means the link was not built by this
			// package, so nothing is shown rather than a sentence with a hole
			// in it.
			return "", ""
		}
		nums = append(nums, clampFlashNum(n))
	}

	msg := entry.text
	if entry.build != nil {
		msg = entry.build(nums)
	}

	// Reasons are looked up the same way the message is: an unknown one is
	// dropped, never printed.
	var reasons []string
	seen := map[string]bool{}
	for _, key := range q[whyParam] {
		if len(reasons) >= maxFlashReasons {
			break
		}
		text, ok := flashReasons[key]
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		reasons = append(reasons, text)
	}
	if len(reasons) > 0 {
		msg = strings.TrimSpace(msg + " " + strings.Join(reasons, " "))
	}

	if entry.kind == flashFailure {
		return "", msg
	}
	return msg, ""
}

// clampFlashNum keeps a count in a range a sentence can hold. A number from a
// URL is not trusted for its size any more than for its meaning.
func clampFlashNum(n int) int {
	if n < 0 {
		return 0
	}
	if n > maxFlashNum {
		return maxFlashNum
	}
	return n
}

func num(n []int, i int) int {
	if i < len(n) {
		return n[i]
	}
	return 0
}

func count(n []int, i int) string {
	return strconv.Itoa(num(n, i))
}
