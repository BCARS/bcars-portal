package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/batches"
	"github.com/bcars/bcars-portal/internal/domain/dues"
	"github.com/bcars/bcars-portal/internal/domain/treasury"
	"github.com/bcars/bcars-portal/internal/domain/worksheets"
)

// The batch grid, posting, review, and correction pages.
//
// The grid is a plain server-rendered table with one form per row. Enter-to-add
// is a small progressive enhancement layered on top; with JavaScript off every
// action is still an ordinary form submit. Totals shown here always come from
// the server, so a client can never disagree with the ledger about the money.

// batchListData lists batches by state.
type batchListData struct {
	State   string
	Batches []batchView
	CanPost bool
}

// batchView is a batch with its money already rendered.
type batchView struct {
	ID        int64
	Label     string
	State     string
	OpenedAt  string
	PostedAt  string
	Entries   int64
	Total     string
	Worksheet int64
}

func batchToView(b batches.Batch) batchView {
	return batchView{
		ID: b.ID, Label: b.Label, State: b.State,
		OpenedAt: b.OpenedAt, PostedAt: b.PostedAt,
		Entries: b.Totals.EntryCount, Total: treasury.Cents(b.Totals.NetTotalCents),
	}
}

// batchList shows open, posted, and abandoned batches.
func (h *Handler) batchList(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	state := r.URL.Query().Get("state")

	rows, err := h.batches.List(r.Context(), p, state, 100, 0)
	if err != nil {
		h.renderDomainError(w, r, err)
		return
	}
	data := batchListData{State: state, CanPost: hasCap(p, "payment.post")}
	for _, b := range rows {
		data.Batches = append(data.Batches, batchToView(b))
	}
	h.render.RenderHTTP(w, "batches.html", http.StatusOK, data)
}

// batchOpen creates a batch and sends the treasurer straight to its grid.
func (h *Handler) batchOpen(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Could not read the form.")
		return
	}
	label := strings.TrimSpace(r.FormValue("label"))
	if label == "" {
		label = fmt.Sprintf("Batch %s", time.Now().UTC().Format(dues.ISODate))
	}

	b, err := h.batches.Open(r.Context(), p, batches.OpenParams{
		Label:          label,
		IdempotencyKey: strings.TrimSpace(r.FormValue("idempotency_key")),
	}, time.Now())
	if err != nil {
		h.renderDomainError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/treasury/batches/%d", b.ID), http.StatusSeeOther)
}

// entryView is one draft row with its money rendered.
type entryView struct {
	ID           int64
	Sequence     int64
	MembershipID int64
	MemberName   string
	Amount       string
	Method       string
	Reference    string
	ReceivedOn   string
	PaidThrough  string
	Note         string
	Version      int64
}

// totalsView is the reconciliation panel: what the treasurer counts against the
// cash tin and the cheques in their hand.
type totalsView struct {
	Entries    int64
	CashCount  int64
	CashTotal  string
	CheckCount int64
	CheckTotal string
	OtherCount int64
	OtherTotal string
	NetTotal   string
}

type batchDetailData struct {
	Batch      batchView
	Version    int64
	Totals     totalsView
	Entries    []entryView
	Defaults   struct{ Amount, PaidThrough string }
	Search     string
	Candidates []dues.Standing
	CanPost    bool
	Error      string
	// IdempotencyKey guards the add-row and post forms against a double submit.
	IdempotencyKey string
	Activity       []treasury.ActivityEntry
	Payments       []postedPaymentView
	// Today prefills a new row's received date.
	Today string

	// Worksheet is the sheet this batch was opened from, if any, and its rows
	// in saved print order. This is the ordered work queue: the treasurer works
	// down the paper, and the page shows where they have got to.
	WorksheetRunID int64
	WorksheetRows  []worksheetQueueRow
	WorksheetDone  int
}

// worksheetQueueRow is one line of the printed sheet as the grid shows it.
// Entered is progress through the saved order, computed from the batch's own
// rows; it never changes the worksheet snapshot.
type worksheetQueueRow struct {
	Ordinal      int64
	MembershipID int64
	DisplayName  string
	CallSign     string
	PaidThrough  string
	DuesStatus   string
	Entered      bool
}

// postedPaymentView is a posted row on the review page. A corrected row shows
// what it was as well as what it is, because hiding the original is exactly
// what an append-only ledger exists to prevent.
type postedPaymentView struct {
	PaymentID   int64
	MemberName  string
	Amount      string
	Method      string
	Reference   string
	ReceivedOn  string
	ReceiptCode string
	Superseded  bool
	EntryKind   string
	CanCorrect  bool
	Revision    int64
}

// batchDetail renders the grid for an open batch, or the review for a terminal
// one. They are one page because they answer the same question at two moments.
func (h *Handler) batchDetail(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid batch id.")
		return
	}
	data, err := h.batchDetailData(r, p, id)
	if err != nil {
		h.renderDomainError(w, r, err)
		return
	}
	h.render.RenderHTTP(w, "batch_detail.html", http.StatusOK, data)
}

func (h *Handler) batchDetailData(r *http.Request, p *authz.Principal, id int64) (batchDetailData, error) {
	b, err := h.batches.Get(r.Context(), p, id)
	if err != nil {
		return batchDetailData{}, err
	}

	data := batchDetailData{
		Batch:          batchToView(b),
		Version:        b.Version,
		CanPost:        hasCap(p, "payment.post"),
		IdempotencyKey: newIdempotencyKey(),
		Today:          time.Now().UTC().Format(dues.ISODate),
		Totals: totalsView{
			Entries:    b.Totals.EntryCount,
			CashCount:  b.Totals.CashCount,
			CashTotal:  treasury.Cents(b.Totals.CashTotalCents),
			CheckCount: b.Totals.CheckCount,
			CheckTotal: treasury.Cents(b.Totals.CheckTotalCents),
			OtherCount: b.Totals.OtherCount,
			OtherTotal: treasury.Cents(b.Totals.OtherTotalCents),
			NetTotal:   treasury.Cents(b.Totals.NetTotalCents),
		},
	}
	if b.DefaultAmountCents != 0 {
		data.Defaults.Amount = treasury.Cents(b.DefaultAmountCents)
	}
	data.Defaults.PaidThrough = b.DefaultPaidThrough

	names := map[int64]string{}
	for _, e := range b.Entries {
		name := names[e.MembershipID]
		if name == "" {
			if st, err := h.dues.GetStanding(r.Context(), p, e.MembershipID, time.Now(), 0); err == nil {
				name = st.DisplayName
				names[e.MembershipID] = name
			}
		}
		data.Entries = append(data.Entries, entryView{
			ID: e.ID, Sequence: e.Sequence, MembershipID: e.MembershipID,
			MemberName: name, Amount: treasury.Cents(e.AmountCents),
			Method: e.Method, Reference: e.Reference, ReceivedOn: e.ReceivedOn,
			PaidThrough: e.PaidThrough, Note: e.TreasurerNote, Version: e.Version,
		})
	}

	// Member search for the grid's add-row, showing safe standing only.
	data.Search = strings.TrimSpace(r.URL.Query().Get("member"))
	if data.Search != "" && b.State == batches.StateOpen {
		candidates, err := h.dues.ListStanding(r.Context(), p, dues.StandingQuery{
			Search: data.Search, Limit: 10,
		})
		if err == nil {
			data.Candidates = candidates
		}
	}

	if b.WorksheetRunID != 0 {
		if err := h.loadWorksheetQueue(r, p, b, &data); err != nil {
			return batchDetailData{}, err
		}
	}

	if b.State != batches.StateOpen {
		if err := h.loadPostedReview(r, p, id, &data); err != nil {
			return batchDetailData{}, err
		}
	}
	return data, nil
}

// loadWorksheetQueue presents the linked sheet's members in saved ordinal
// order, marking the ones already entered in this batch.
func (h *Handler) loadWorksheetQueue(r *http.Request, p *authz.Principal, b batches.Batch, data *batchDetailData) error {
	rows, err := h.worksheets.Rows(r.Context(), p, b.WorksheetRunID, worksheets.MaxRows, 0)
	if err != nil {
		// A treasurer without worksheet access can still work the batch; the
		// ordered queue is a convenience, not the batch itself.
		if errors.Is(err, authz.ErrDenied) {
			return nil
		}
		return err
	}

	entered := map[int64]bool{}
	for _, e := range b.Entries {
		entered[e.MembershipID] = true
	}

	data.WorksheetRunID = b.WorksheetRunID
	for _, row := range rows {
		view := worksheetQueueRow{
			Ordinal: row.Ordinal, MembershipID: row.MembershipID,
			DisplayName: row.DisplayName, CallSign: row.CallSign,
			PaidThrough: row.PaidThrough, DuesStatus: row.DuesStatus,
			Entered: entered[row.MembershipID],
		}
		if view.Entered {
			data.WorksheetDone++
		}
		data.WorksheetRows = append(data.WorksheetRows, view)
	}
	return nil
}

// loadPostedReview fills the review half of the page: what was posted, what has
// since been corrected, and the plain-language history.
func (h *Handler) loadPostedReview(r *http.Request, p *authz.Principal, batchID int64, data *batchDetailData) error {
	if !hasCap(p, "payment.read") {
		return nil
	}

	activity, err := h.treasury.BatchActivity(r.Context(), p, batchID)
	if err != nil {
		return err
	}
	// Newest first: the last thing that happened is what a treasurer is asking
	// about when they open a posted batch.
	for i := len(activity) - 1; i >= 0; i-- {
		data.Activity = append(data.Activity, activity[i])
	}

	entries, err := h.treasury.ListLedger(r.Context(), p, treasury.LedgerQuery{
		BatchID: batchID, Limit: 200,
	})
	if err != nil {
		return err
	}
	canCorrect := hasCap(p, "payment.correct")
	for _, e := range entries {
		view := postedPaymentView{
			PaymentID: e.PaymentID, MemberName: e.DisplayName,
			Amount: treasury.Cents(e.AmountCents), Method: e.Method,
			Reference: e.Reference, ReceivedOn: e.ReceivedOn,
			ReceiptCode: e.ReceiptCode, Superseded: e.Superseded,
			EntryKind: e.EntryKind,
		}
		// Only a row still in force can be corrected, and a reversal never can.
		view.CanCorrect = canCorrect && !e.Superseded && e.EntryKind != "reversal"
		if view.CanCorrect {
			if rev, err := h.batches.ChainRevision(r.Context(), p, e.PaymentID); err == nil {
				view.Revision = rev
			}
		}
		data.Payments = append(data.Payments, view)
	}
	return nil
}

// batchUpdateDefaults persists the values the grid prefills into a new row.
func (h *Handler) batchUpdateDefaults(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid batch id.")
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Could not read the form.")
		return
	}

	var amount int64
	if raw := strings.TrimSpace(r.FormValue("default_amount")); raw != "" {
		amount, err = parseAmountCents(raw)
		if err != nil {
			h.renderBatchError(w, r, p, id, formMessage(err))
			return
		}
	}
	version, _ := strconv.ParseInt(r.FormValue("version"), 10, 64)

	_, err = h.batches.Update(r.Context(), p, id, batches.UpdateParams{
		Label:              strings.TrimSpace(r.FormValue("label")),
		DefaultAmountCents: amount,
		DefaultPaidThrough: strings.TrimSpace(r.FormValue("default_paid_through")),
		ExpectedVersion:    version,
	})
	if err != nil {
		if msg, ok := batchFormMessage(err); ok {
			h.renderBatchError(w, r, p, id, msg)
			return
		}
		h.renderDomainError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/treasury/batches/%d", id), http.StatusSeeOther)
}

// batchAddEntry appends a draft row.
func (h *Handler) batchAddEntry(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid batch id.")
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Could not read the form.")
		return
	}

	membershipID, _ := strconv.ParseInt(r.FormValue("membership_id"), 10, 64)
	cents, err := parseAmountCents(r.FormValue("amount"))
	if err != nil {
		h.renderBatchError(w, r, p, id, formMessage(err))
		return
	}

	_, _, err = h.batches.AddEntry(r.Context(), p, id, batches.EntryInput{
		MembershipID:      membershipID,
		AmountCents:       cents,
		Method:            r.FormValue("method"),
		Reference:         strings.TrimSpace(r.FormValue("reference")),
		ReceivedOn:        strings.TrimSpace(r.FormValue("received_on")),
		ReceivedByOfficer: strings.TrimSpace(r.FormValue("received_by_officer")),
		PaidThrough:       strings.TrimSpace(r.FormValue("paid_through")),
		TreasurerNote:     strings.TrimSpace(r.FormValue("note")),
	}, strings.TrimSpace(r.FormValue("idempotency_key")))
	if err != nil {
		if msg, ok := batchFormMessage(err); ok {
			h.renderBatchError(w, r, p, id, msg)
			return
		}
		h.renderDomainError(w, r, err)
		return
	}
	// Back to the grid with the search box cleared and ready for the next row.
	http.Redirect(w, r, fmt.Sprintf("/admin/treasury/batches/%d#add-row", id), http.StatusSeeOther)
}

// batchDeleteEntry removes a draft row.
func (h *Handler) batchDeleteEntry(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid batch id.")
		return
	}
	entryID, err := strconv.ParseInt(r.PathValue("entry_id"), 10, 64)
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid row id.")
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Could not read the form.")
		return
	}
	version, _ := strconv.ParseInt(r.FormValue("version"), 10, 64)

	if _, err := h.batches.DeleteEntry(r.Context(), p, id, entryID, version); err != nil {
		if msg, ok := batchFormMessage(err); ok {
			h.renderBatchError(w, r, p, id, msg)
			return
		}
		h.renderDomainError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/treasury/batches/%d", id), http.StatusSeeOther)
}

// batchPost posts the batch. The form carries the batch version, so a batch
// that changed since the page was rendered is refused rather than posted blind.
func (h *Handler) batchPost(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid batch id.")
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Could not read the form.")
		return
	}
	version, _ := strconv.ParseInt(r.FormValue("version"), 10, 64)

	if _, err := h.batches.Post(r.Context(), p, id, batches.PostParams{
		ExpectedVersion: version,
		IdempotencyKey:  strings.TrimSpace(r.FormValue("idempotency_key")),
		Confirm:         r.FormValue("confirm") == "yes",
	}, time.Now()); err != nil {
		if msg, ok := batchFormMessage(err); ok {
			h.renderBatchError(w, r, p, id, msg)
			return
		}
		h.renderDomainError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/treasury/batches/%d", id), http.StatusSeeOther)
}

// batchAbandon closes a batch without posting it.
func (h *Handler) batchAbandon(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid batch id.")
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Could not read the form.")
		return
	}
	version, _ := strconv.ParseInt(r.FormValue("version"), 10, 64)

	if _, err := h.batches.Abandon(r.Context(), p, id,
		strings.TrimSpace(r.FormValue("reason")), version, time.Now()); err != nil {
		if msg, ok := batchFormMessage(err); ok {
			h.renderBatchError(w, r, p, id, msg)
			return
		}
		h.renderDomainError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/treasury/batches/%d", id), http.StatusSeeOther)
}

// receiptData is the printable slip for one payment.
type receiptData struct {
	Receipt treasury.Receipt
	Amount  string
}

// receiptPage renders a printable receipt.
func (h *Handler) receiptPage(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	paymentID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid payment id.")
		return
	}
	receipt, err := h.treasury.GetReceipt(r.Context(), p, paymentID)
	if err != nil {
		h.renderDomainError(w, r, err)
		return
	}
	h.render.RenderHTTP(w, "receipt.html", http.StatusOK, receiptData{
		Receipt: receipt, Amount: treasury.Cents(receipt.AmountCents),
	})
}

// correctionFormData drives the "Fix this" dialog.
type correctionFormData struct {
	Payment     postedPaymentView
	BatchID     int64
	Revision    int64
	Amount      string
	Method      string
	Reference   string
	ReceivedOn  string
	PaidThrough string
	Reason      string
	Error       string
	// Consequence explains, without ledger jargon, what saving will do.
	Consequence    string
	IdempotencyKey string
}

// correctionForm renders the correction dialog for one posted payment.
func (h *Handler) correctionForm(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	paymentID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid payment id.")
		return
	}
	data, err := h.correctionBase(r, p, paymentID)
	if err != nil {
		h.renderDomainError(w, r, err)
		return
	}
	h.render.RenderHTTP(w, "correction.html", http.StatusOK, data)
}

func (h *Handler) correctionBase(r *http.Request, p *authz.Principal, paymentID int64) (correctionFormData, error) {
	receipt, err := h.treasury.GetReceipt(r.Context(), p, paymentID)
	if err != nil {
		return correctionFormData{}, err
	}
	revision, err := h.batches.ChainRevision(r.Context(), p, paymentID)
	if err != nil {
		return correctionFormData{}, err
	}

	return correctionFormData{
		Payment: postedPaymentView{
			PaymentID: receipt.PaymentID, MemberName: receipt.DisplayName,
			Amount: treasury.Cents(receipt.AmountCents), Method: receipt.Method,
			Reference: receipt.Reference, ReceivedOn: receipt.ReceivedOn,
			ReceiptCode: receipt.ReceiptCode, Superseded: receipt.Superseded,
		},
		BatchID:        receipt.BatchID,
		Revision:       revision,
		Amount:         treasury.Cents(receipt.AmountCents),
		Method:         receipt.Method,
		Reference:      receipt.Reference,
		ReceivedOn:     receipt.ReceivedOn,
		PaidThrough:    receipt.PaidThrough,
		IdempotencyKey: newIdempotencyKey(),
		Consequence: fmt.Sprintf(
			"The original entry of $%s stays in the records. A matching reversal and "+
				"the corrected entry are added, so the books show what happened and what it became.",
			treasury.Cents(receipt.AmountCents)),
	}, nil
}

// correctionSubmit appends the correction.
func (h *Handler) correctionSubmit(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	paymentID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid payment id.")
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Could not read the form.")
		return
	}

	data, err := h.correctionBase(r, p, paymentID)
	if err != nil {
		h.renderDomainError(w, r, err)
		return
	}
	data.Amount = strings.TrimSpace(r.FormValue("amount"))
	data.Method = r.FormValue("method")
	data.Reference = strings.TrimSpace(r.FormValue("reference"))
	data.ReceivedOn = strings.TrimSpace(r.FormValue("received_on"))
	data.PaidThrough = strings.TrimSpace(r.FormValue("paid_through"))
	data.Reason = strings.TrimSpace(r.FormValue("reason"))
	if key := strings.TrimSpace(r.FormValue("idempotency_key")); key != "" {
		data.IdempotencyKey = key
	}
	revision, _ := strconv.ParseInt(r.FormValue("revision"), 10, 64)

	cents, err := parseAmountCents(data.Amount)
	if err != nil {
		data.Error = formMessage(err)
		h.render.RenderHTTP(w, "correction.html", http.StatusUnprocessableEntity, data)
		return
	}

	result, err := h.batches.CorrectPayment(r.Context(), p, paymentID, batches.CorrectParams{
		AmountCents:      cents,
		Method:           data.Method,
		Reference:        data.Reference,
		ReceivedOn:       data.ReceivedOn,
		PaidThrough:      data.PaidThrough,
		Reason:           data.Reason,
		ExpectedRevision: revision,
		IdempotencyKey:   data.IdempotencyKey,
		Confirm:          true,
	}, time.Now())
	if err != nil {
		if msg, ok := batchFormMessage(err); ok {
			data.Error = msg
			h.render.RenderHTTP(w, "correction.html", http.StatusUnprocessableEntity, data)
			return
		}
		h.renderDomainError(w, r, err)
		return
	}

	if result.Batch.ID != 0 {
		http.Redirect(w, r, fmt.Sprintf("/admin/treasury/batches/%d", result.Batch.ID), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/treasury/batches", http.StatusSeeOther)
}

// renderBatchError re-renders the batch page carrying a message the treasurer
// can act on, rather than replacing their work with an error screen.
func (h *Handler) renderBatchError(w http.ResponseWriter, r *http.Request, p *authz.Principal, id int64, msg string) {
	data, err := h.batchDetailData(r, p, id)
	if err != nil {
		h.renderDomainError(w, r, err)
		return
	}
	data.Error = msg
	h.render.RenderHTTP(w, "batch_detail.html", http.StatusUnprocessableEntity, data)
}

// batchFormMessage turns a domain error into a sentence for an officer. It
// returns false for anything that is not the treasurer's to fix.
func batchFormMessage(err error) (string, bool) {
	switch {
	case errors.Is(err, db.ErrStale):
		return "Someone else changed this batch while you were working. " +
			"Reload the page and check the rows before posting.", true
	case errors.Is(err, batches.ErrBatchNotOpen):
		return "This batch has already been posted or abandoned, so it cannot be changed.", true
	case errors.Is(err, batches.ErrEmptyBatch):
		return "There is nothing to post. Add at least one row first.", true
	case errors.Is(err, batches.ErrConfirmationRequired):
		return "Tick the confirmation box before posting.", true
	case errors.Is(err, batches.ErrReasonRequired):
		return "Say why, so the records explain themselves later.", true
	case errors.Is(err, batches.ErrPaymentSuperseded):
		return "This entry has already been corrected. Fix the corrected entry instead.", true
	case errors.Is(err, batches.ErrLabelRequired):
		return "Give the batch a name you will recognise later.", true
	}
	if msg, ok := paymentFormMessage(err); ok {
		return msg, true
	}
	return "", false
}
