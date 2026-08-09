package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/batches"
	"github.com/bcars/bcars-portal/internal/domain/dues"
	"github.com/bcars/bcars-portal/internal/domain/treasury"
)

// The treasurer's pages.
//
// These are thin adapters over the Phase 2 services. No business rule,
// authorization decision, or persistence path lives here: the page renders what
// a service returned and posts back through the same operation the API uses. In
// particular the money and the coverage date are two separate fields all the
// way down, because they are two separate facts.

// statusCountsLimit bounds the standing sweep the summary counts. Club scale is
// a few hundred members; a roster past this needs a counting query rather than
// a page that silently under-reports.
const statusCountsLimit = 2000

// pageSize is the standing list page size.
const pageSize = 25

// statusOrder fixes the order the summary presents, worst first: the treasurer
// opens this page to find who needs chasing.
var statusOrder = []string{
	dues.StatusExpired,
	dues.StatusUnknown,
	dues.StatusExpiring,
	dues.StatusCurrent,
	dues.StatusHonoraryWaived,
}

// statusLabels are the plain-language names shown to officers. Ledger
// vocabulary stays in the code.
var statusLabels = map[string]string{
	dues.StatusExpired:        "Dues expired",
	dues.StatusUnknown:        "No dues recorded",
	dues.StatusExpiring:       "Expiring soon",
	dues.StatusCurrent:        "Dues current",
	dues.StatusHonoraryWaived: "Dues waived",
}

type statusCount struct {
	Status string
	Label  string
	Count  int
}

type treasuryHomeData struct {
	AsOf     string
	Counts   []statusCount
	Total    int
	Overflow bool
	CanPost  bool
}

// treasuryHome shows how the club's dues stand today.
func (h *Handler) treasuryHome(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	asOf := time.Now().UTC()

	rows, err := h.dues.ListStanding(r.Context(), p, dues.StandingQuery{
		AsOf: asOf, Limit: statusCountsLimit,
	})
	if err != nil {
		h.renderDomainError(w, r, err)
		return
	}

	counts := map[string]int{}
	for _, row := range rows {
		counts[row.Status]++
	}

	data := treasuryHomeData{
		AsOf:     asOf.Format(dues.ISODate),
		Total:    len(rows),
		Overflow: len(rows) >= statusCountsLimit,
		CanPost:  hasCap(p, "payment.post"),
	}
	for _, s := range statusOrder {
		data.Counts = append(data.Counts, statusCount{Status: s, Label: statusLabels[s], Count: counts[s]})
	}
	h.render.RenderHTTP(w, "treasury_home.html", http.StatusOK, data)
}

type standingListData struct {
	Status    string
	Label     string
	AsOf      string
	Rows      []dues.Standing
	Query     string
	Page      int
	HasNext   bool
	HasPrev   bool
	NextPage  int
	PrevPage  int
	CanPost   bool
	StatusNav []statusCount
}

// treasuryStanding lists members in one standing.
func (h *Handler) treasuryStanding(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	asOf := time.Now().UTC()

	status := r.URL.Query().Get("status")
	if status != "" && statusLabels[status] == "" {
		h.renderError(w, r, http.StatusBadRequest, "Unknown dues status.")
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("q"))

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	offset := int64((page - 1) * pageSize)

	// Fetch one extra row to know whether a next page exists without counting.
	rows, err := h.dues.ListStanding(r.Context(), p, dues.StandingQuery{
		AsOf: asOf, Status: status, Search: search,
		Limit: pageSize + 1, Offset: offset,
	})
	if err != nil {
		h.renderDomainError(w, r, err)
		return
	}
	hasNext := len(rows) > pageSize
	if hasNext {
		rows = rows[:pageSize]
	}

	label := "All members"
	if status != "" {
		label = statusLabels[status]
	}
	data := standingListData{
		Status: status, Label: label, AsOf: asOf.Format(dues.ISODate),
		Rows: rows, Query: search, Page: page,
		HasNext: hasNext, HasPrev: page > 1,
		NextPage: page + 1, PrevPage: page - 1,
		CanPost: hasCap(p, "payment.post"),
	}
	for _, s := range statusOrder {
		data.StatusNav = append(data.StatusNav, statusCount{Status: s, Label: statusLabels[s]})
	}
	h.render.RenderHTTP(w, "treasury_standing.html", http.StatusOK, data)
}

// paymentFormData is everything the payment screen needs. Amount and
// paid-through are deliberately separate fields, and the page says so.
type paymentFormData struct {
	Standing    dues.Standing
	Suggestions []suggestionView
	History     []historyView
	CanSeeMoney bool

	// IdempotencyKey is minted per form render, so the browser's back button
	// and a double click cannot post the same payment twice.
	IdempotencyKey string

	// Form values, preserved across a validation error.
	Amount      string
	Method      string
	Reference   string
	ReceivedOn  string
	Officer     string
	PaidThrough string
	Note        string

	Error string
	// Consequence is the plain-language sentence shown before saving.
	Consequence string
	Saved       bool
	ReceiptCode string
}

// suggestionView is a suggestion with its amount already rendered, so money
// formatting lives in one place rather than in a template.
type suggestionView struct {
	Label       string
	PaidThrough string
	Amount      string
	RateKnown   bool
	Explanation string
}

// historyView is a prior payment with its amount already rendered.
type historyView struct {
	ReceivedOn  string
	Amount      string
	Method      string
	Reference   string
	ReceiptCode string
}

// treasuryPaymentForm renders the single-payment screen for one membership.
func (h *Handler) treasuryPaymentForm(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid member id.")
		return
	}

	data, err := h.paymentFormBase(r, p, id)
	if err != nil {
		h.renderDomainError(w, r, err)
		return
	}
	h.render.RenderHTTP(w, "treasury_payment.html", http.StatusOK, data)
}

// paymentFormBase loads the standing, suggestions, and prior history a payment
// screen shows. History is only loaded when the caller may see money.
func (h *Handler) paymentFormBase(r *http.Request, p *authz.Principal, membershipID int64) (paymentFormData, error) {
	now := time.Now().UTC()

	standing, err := h.dues.GetStanding(r.Context(), p, membershipID, now, 0)
	if err != nil {
		return paymentFormData{}, err
	}

	data := paymentFormData{
		Standing:       standing,
		CanSeeMoney:    hasCap(p, "payment.read"),
		IdempotencyKey: newIdempotencyKey(),
		Method:         batches.MethodCash,
		ReceivedOn:     now.Format(dues.ISODate),
	}

	if suggestions, err := h.dues.SuggestFor(r.Context(), p, now); err == nil {
		for _, c := range suggestions.Choices {
			view := suggestionView{
				Label: c.Label, PaidThrough: c.PaidThrough,
				RateKnown: c.RateKnown, Explanation: c.Explanation,
			}
			if c.RateKnown {
				view.Amount = treasury.Cents(c.AmountCents)
			}
			data.Suggestions = append(data.Suggestions, view)
		}
		if len(suggestions.Choices) > 0 {
			// Prefill only. The treasurer's typed value always wins, and the
			// server never substitutes this for an omitted field.
			data.PaidThrough = suggestions.Choices[0].PaidThrough
			if suggestions.Choices[0].RateKnown {
				data.Amount = treasury.Cents(suggestions.Choices[0].AmountCents)
			}
		}
	}

	if data.CanSeeMoney {
		history, err := h.treasury.ListLedger(r.Context(), p, treasury.LedgerQuery{
			MembershipID: membershipID, EffectiveOnly: true, Limit: 10,
		})
		if err != nil {
			return paymentFormData{}, err
		}
		for _, e := range history {
			data.History = append(data.History, historyView{
				ReceivedOn: e.ReceivedOn, Amount: treasury.Cents(e.AmountCents),
				Method: e.Method, Reference: e.Reference, ReceiptCode: e.ReceiptCode,
			})
		}
	}
	return data, nil
}

// treasuryPaymentSubmit records one payment through the same operation the API
// uses. There is no second persistence path.
func (h *Handler) treasuryPaymentSubmit(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid member id.")
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Could not read the form.")
		return
	}

	data, err := h.paymentFormBase(r, p, id)
	if err != nil {
		h.renderDomainError(w, r, err)
		return
	}

	// Echo what was typed so a validation error never costs the treasurer
	// their entry.
	data.Amount = strings.TrimSpace(r.FormValue("amount"))
	data.Method = r.FormValue("method")
	data.Reference = strings.TrimSpace(r.FormValue("reference"))
	data.ReceivedOn = strings.TrimSpace(r.FormValue("received_on"))
	data.Officer = strings.TrimSpace(r.FormValue("received_by_officer"))
	data.PaidThrough = strings.TrimSpace(r.FormValue("paid_through"))
	data.Note = strings.TrimSpace(r.FormValue("note"))
	if key := strings.TrimSpace(r.FormValue("idempotency_key")); key != "" {
		data.IdempotencyKey = key
	}

	cents, err := parseAmountCents(data.Amount)
	if err != nil {
		data.Error = formMessage(err)
		h.render.RenderHTTP(w, "treasury_payment.html", http.StatusUnprocessableEntity, data)
		return
	}

	result, err := h.batches.PostSinglePayment(r.Context(), p, batches.SingleParams{
		Entry: batches.EntryInput{
			MembershipID:      id,
			AmountCents:       cents,
			Method:            data.Method,
			Reference:         data.Reference,
			ReceivedOn:        data.ReceivedOn,
			ReceivedByOfficer: data.Officer,
			PaidThrough:       data.PaidThrough,
			TreasurerNote:     data.Note,
		},
		Label:          fmt.Sprintf("Payment recorded %s", time.Now().UTC().Format(dues.ISODate)),
		IdempotencyKey: data.IdempotencyKey,
		Confirm:        true,
	}, time.Now())
	if err != nil {
		if msg, ok := paymentFormMessage(err); ok {
			data.Error = msg
			h.render.RenderHTTP(w, "treasury_payment.html", http.StatusUnprocessableEntity, data)
			return
		}
		h.renderDomainError(w, r, err)
		return
	}

	// Reload so the page shows the new standing and history rather than the
	// state that led here.
	fresh, err := h.paymentFormBase(r, p, id)
	if err != nil {
		h.renderDomainError(w, r, err)
		return
	}
	fresh.Saved = true
	if len(result.Payments) > 0 {
		fresh.ReceiptCode = result.Payments[0].ReceiptCode
	}
	fresh.Consequence = consequenceSentence(cents, result.Coverage)
	h.render.RenderHTTP(w, "treasury_payment.html", http.StatusOK, fresh)
}

// consequenceSentence states, in plain language, what was just recorded. It
// names the money and the coverage separately because they are separate facts.
func consequenceSentence(cents int64, coverage []batches.Coverage) string {
	if len(coverage) == 0 {
		return fmt.Sprintf("Recorded $%s. Dues paid through was not changed.", treasury.Cents(cents))
	}
	return fmt.Sprintf("Recorded $%s and set Dues paid through to %s.",
		treasury.Cents(cents), coverage[0].PaidThrough)
}

// formError carries a sentence written for an officer to read. Error() stays
// lowercase and unpunctuated to satisfy Go's convention, while Message is what
// the page shows, so the copy lives in exactly one place.
type formError struct{ Message string }

func (e formError) Error() string {
	return strings.ToLower(strings.TrimSuffix(e.Message, "."))
}

// parseAmountCents reads a dollar amount as integer cents without float
// arithmetic, so what the treasurer typed is what the ledger stores.
func parseAmountCents(s string) (int64, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "$"))
	s = strings.ReplaceAll(s, ",", "")
	if s == "" {
		return 0, formError{Message: "Enter the amount received."}
	}
	whole, frac, hasFrac := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	dollars, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || dollars < 0 {
		return 0, formError{Message: "Enter the amount as a number, for example 40 or 40.00."}
	}
	cents := int64(0)
	if hasFrac {
		switch len(frac) {
		case 1:
			frac += "0"
		case 2:
		default:
			return 0, formError{Message: "Enter at most two decimal places, for example 40.00."}
		}
		cents, err = strconv.ParseInt(frac, 10, 64)
		if err != nil || cents < 0 {
			return 0, formError{Message: "Enter the amount as a number, for example 40 or 40.00."}
		}
	}
	total := dollars*100 + cents
	if total <= 0 {
		return 0, formError{Message: "The amount must be greater than zero."}
	}
	return total, nil
}

// formMessage renders an error as the sentence to show on the page.
func formMessage(err error) string {
	var fe formError
	if errors.As(err, &fe) {
		return fe.Message
	}
	return "Could not save that entry. Please check the form and try again."
}

// paymentFormMessage turns a domain error into something an officer can act on.
// It returns false for errors that are not the treasurer's fault.
func paymentFormMessage(err error) (string, bool) {
	switch {
	case errors.Is(err, batches.ErrInvalidAmount):
		return "The amount must be greater than zero.", true
	case errors.Is(err, batches.ErrInvalidMethod):
		return "Choose cash, check, or other.", true
	case errors.Is(err, batches.ErrInvalidDate):
		return "Enter dates as YYYY-MM-DD, for example 2026-12-31.", true
	case errors.Is(err, dues.ErrInvalidDate):
		return "Enter dates as YYYY-MM-DD, for example 2026-12-31.", true
	}
	return "", false
}

// newIdempotencyKey mints a key for one form render.
func newIdempotencyKey() string {
	return fmt.Sprintf("web-%d", time.Now().UTC().UnixNano())
}

func hasCap(p *authz.Principal, code string) bool {
	if p == nil {
		return false
	}
	_, ok := p.Capabilities[code]
	return ok
}
