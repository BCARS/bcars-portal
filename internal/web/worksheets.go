package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/dues"
	"github.com/bcars/bcars-portal/internal/domain/treasury"
	"github.com/bcars/bcars-portal/internal/domain/worksheets"
)

// The printable renewal worksheet.
//
// Browser printing is the whole delivery mechanism: no PDF library, no external
// service. The print stylesheet does the work, which means the page a treasurer
// reads on screen and the paper they carry to the meeting are the same
// document.

// maxGuestRows bounds the blank rows a treasurer can ask for. Guest rows are
// for walk-ins who are not yet members; a sheet is paper, so this is a page or
// two of them at most.
const maxGuestRows = 20

// duesYearEndRule is printed on the sheet so anyone filling it in by hand knows
// the club convention without having to ask.
const duesYearEndRule = "The club dues year ends 31 December. Dues paid through is normally a 31 December date, " +
	"but the treasurer may record any date that reflects what was actually agreed."

// worksheetOptionsData drives the options form for both the initial render and
// a validation failure. One view model means the fields a rejected submission
// preserves cannot drift from the fields the form offers.
type worksheetOptionsData struct {
	Runs  []worksheetRunView
	Error string
	Today string

	// Submitted values, echoed back so a rejected form does not discard the
	// treasurer's choices.
	Label        string
	AsOf         string
	FilterKind   string
	SourceRunID  int64
	SortOrder    string
	IncludeEmail bool
	IncludePhone bool
	GuestRows    string
}

// defaults fills the choices a fresh form starts from.
func (d worksheetOptionsData) defaults() worksheetOptionsData {
	if d.FilterKind == "" {
		d.FilterKind = worksheets.FilterOwes
	}
	if d.SortOrder == "" {
		d.SortOrder = worksheets.SortLastName
	}
	if d.AsOf == "" {
		d.AsOf = d.Today
	}
	if d.GuestRows == "" {
		d.GuestRows = "3"
	}
	return d
}

// optionsFromForm reads the submitted options back into the view model.
func optionsFromForm(r *http.Request, today string) worksheetOptionsData {
	sourceRunID, _ := strconv.ParseInt(r.FormValue("source_run_id"), 10, 64)
	return worksheetOptionsData{
		Today:        today,
		Label:        strings.TrimSpace(r.FormValue("label")),
		AsOf:         strings.TrimSpace(r.FormValue("as_of")),
		FilterKind:   r.FormValue("filter_kind"),
		SourceRunID:  sourceRunID,
		SortOrder:    r.FormValue("sort_order"),
		IncludeEmail: r.FormValue("include_email") == "yes",
		IncludePhone: r.FormValue("include_phone") == "yes",
		GuestRows:    strings.TrimSpace(r.FormValue("guest_rows")),
	}
}

type worksheetRunView struct {
	ID          int64
	Label       string
	AsOf        string
	FilterKind  string
	SortOrder   string
	GeneratedAt string
	RowCount    int64
}

func worksheetRunToView(r worksheets.Run) worksheetRunView {
	label := r.Label
	if label == "" {
		label = fmt.Sprintf("Sheet %d", r.ID)
	}
	return worksheetRunView{
		ID: r.ID, Label: label, AsOf: r.AsOf, FilterKind: r.FilterKind,
		SortOrder: r.SortOrder, GeneratedAt: r.GeneratedAt, RowCount: r.RowCount,
	}
}

// worksheetOptions offers the choices and lists previous sheets.
func (h *Handler) worksheetOptions(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)

	runs, err := h.worksheets.List(r.Context(), p, 50, 0)
	if err != nil {
		h.renderDomainError(w, r, err)
		return
	}
	data := worksheetOptionsData{Today: time.Now().UTC().Format(dues.ISODate)}.defaults()
	for _, run := range runs {
		data.Runs = append(data.Runs, worksheetRunToView(run))
	}
	h.render.RenderHTTP(w, "worksheet_options.html", http.StatusOK, data)
}

// worksheetCreate generates a durable run and shows it.
func (h *Handler) worksheetCreate(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Could not read the form.")
		return
	}

	submitted := optionsFromForm(r, time.Now().UTC().Format(dues.ISODate))

	// A durable report parameter is never silently repaired. An omitted date
	// means today, which is the documented default the API shares; anything
	// else that will not parse is a validation failure, because a worksheet
	// judged against a date nobody submitted looks perfectly valid afterwards.
	var asOf time.Time
	if submitted.AsOf != "" {
		parsed, err := time.Parse(dues.ISODate, submitted.AsOf)
		if err != nil {
			h.renderWorksheetOptionsError(w, r, p, submitted,
				"Enter the as-of date as YYYY-MM-DD, for example 2026-07-01.")
			return
		}
		asOf = parsed
	}

	run, _, err := h.worksheets.Create(r.Context(), p, worksheets.CreateParams{
		Label:        submitted.Label,
		AsOf:         asOf,
		FilterKind:   submitted.FilterKind,
		SourceRunID:  submitted.SourceRunID,
		SortOrder:    submitted.SortOrder,
		IncludeEmail: submitted.IncludeEmail,
		IncludePhone: submitted.IncludePhone,
	}, time.Now())
	if err != nil {
		h.renderWorksheetOptionsError(w, r, p, submitted, worksheetErrorMessage(err))
		return
	}

	target := fmt.Sprintf("/admin/treasury/worksheets/%d", run.ID)
	if submitted.GuestRows != "" {
		target += "?guests=" + submitted.GuestRows
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// worksheetRowView is one printed line.
type worksheetRowView struct {
	Ordinal      int64
	MembershipID int64
	DisplayName  string
	CallSign     string
	Contact      string
	DuesStatus   string
	PaidThrough  string
	EnteredSince bool
}

type worksheetSheetData struct {
	Run         worksheetRunView
	Rows        []worksheetRowView
	GuestRows   []int
	AnnualRate  string
	RateYear    int
	YearEndRule string
	PrintedAt   string
	GoodAsOf    string
	ShowContact bool
	ContactKind string
	// FollowUp is true when this sheet followed an earlier one, in which case
	// the entered-since column is the point of reading it.
	FollowUp   bool
	EnteredAny bool
	CanBatch   bool
	Error      string
	// HandoffKey makes "Enter this sheet now" retry-safe: resubmitting the
	// rendered form returns the batch it already opened.
	HandoffKey string
}

// worksheetSheet renders the printable sheet.
func (h *Handler) worksheetSheet(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid sheet id.")
		return
	}
	data, err := h.worksheetSheetData(r, p, id)
	if err != nil {
		h.renderDomainError(w, r, err)
		return
	}
	h.render.RenderHTTP(w, "worksheet_sheet.html", http.StatusOK, data)
}

func (h *Handler) worksheetSheetData(r *http.Request, p *authz.Principal, id int64) (worksheetSheetData, error) {
	run, err := h.worksheets.Get(r.Context(), p, id)
	if err != nil {
		return worksheetSheetData{}, err
	}
	rows, err := h.worksheets.Rows(r.Context(), p, id, worksheets.MaxRows, 0)
	if err != nil {
		return worksheetSheetData{}, err
	}

	now := time.Now().UTC()
	data := worksheetSheetData{
		Run:         worksheetRunToView(run),
		YearEndRule: duesYearEndRule,
		PrintedAt:   now.Format(dues.ISODate),
		// The run's generation time is what the snapshot is good as of, not
		// today: the contact details on paper are as they stood then.
		GoodAsOf:    run.GeneratedAt,
		ShowContact: run.IncludeEmail || run.IncludePhone,
		FollowUp:    run.FilterKind == worksheets.FilterUnpaidSinceRun,
		CanBatch:    hasCap(p, "payment.batch.manage"),
		HandoffKey:  fmt.Sprintf("worksheet-%d-handoff", run.ID),
		RateYear:    now.Year(),
		AnnualRate:  "not set",
	}
	switch {
	case run.IncludeEmail && run.IncludePhone:
		data.ContactKind = "Email / phone"
	case run.IncludeEmail:
		data.ContactKind = "Email"
	case run.IncludePhone:
		data.ContactKind = "Phone"
	}

	if rate, err := h.dues.GetRate(r.Context(), p, int64(now.Year())); err == nil {
		data.AnnualRate = treasury.Cents(rate.AmountCents)
	}

	for _, row := range rows {
		view := worksheetRowView{
			Ordinal: row.Ordinal, MembershipID: row.MembershipID,
			DisplayName: row.DisplayName, CallSign: row.CallSign,
			DuesStatus: row.DuesStatus, PaidThrough: row.PaidThrough,
			EnteredSince: row.EnteredSince,
		}
		// Contact columns are only present when the run recorded them, and a
		// member with none reads as text rather than an empty box nobody can
		// interpret.
		if data.ShowContact {
			parts := []string{}
			if run.IncludeEmail && row.Email != "" {
				parts = append(parts, row.Email)
			}
			if run.IncludePhone && row.Phone != "" {
				parts = append(parts, row.Phone)
			}
			view.Contact = strings.Join(parts, " / ")
			if view.Contact == "" {
				view.Contact = "Not shared"
			}
		}
		if row.EnteredSince {
			data.EnteredAny = true
		}
		data.Rows = append(data.Rows, view)
	}

	if guests, err := strconv.Atoi(r.URL.Query().Get("guests")); err == nil && guests > 0 {
		if guests > maxGuestRows {
			guests = maxGuestRows
		}
		for i := 1; i <= guests; i++ {
			data.GuestRows = append(data.GuestRows, i)
		}
	}
	return data, nil
}

// worksheetBatch opens a batch for this sheet and links it, so the grid can be
// worked in the order the paper was printed.
func (h *Handler) worksheetBatch(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid sheet id.")
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Could not read the form.")
		return
	}

	// One operation creates and links the batch, so a retried submission
	// returns the same batch instead of opening a second empty one.
	batchID, err := h.worksheets.OpenBatchForRun(r.Context(), p, id,
		strings.TrimSpace(r.FormValue("label")),
		strings.TrimSpace(r.FormValue("idempotency_key")),
		time.Now())
	if err != nil {
		h.renderDomainError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/treasury/batches/%d", batchID), http.StatusSeeOther)
}

func (h *Handler) renderWorksheetOptionsError(w http.ResponseWriter, r *http.Request, p *authz.Principal, submitted worksheetOptionsData, msg string) {
	runs, err := h.worksheets.List(r.Context(), p, 50, 0)
	if err != nil {
		h.renderDomainError(w, r, err)
		return
	}
	data := submitted.defaults()
	data.Error = msg
	for _, run := range runs {
		data.Runs = append(data.Runs, worksheetRunToView(run))
	}
	h.render.RenderHTTP(w, "worksheet_options.html", http.StatusUnprocessableEntity, data)
}

func worksheetErrorMessage(err error) string {
	switch {
	case err == nil:
		return ""
	case strings.Contains(err.Error(), "unknown filter"):
		return "Choose who the sheet should list."
	case strings.Contains(err.Error(), "unknown sort"):
		return "Choose how the sheet should be sorted."
	case strings.Contains(err.Error(), "needs a source run"):
		return "Choose which earlier sheet to follow up on."
	case strings.Contains(err.Error(), "too many rows"):
		return "That would print far too many pages. Narrow the sheet first."
	}
	return "Could not generate that sheet. Check the options and try again."
}
