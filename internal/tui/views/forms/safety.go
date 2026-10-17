package forms

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"warte/internal/core"
	"warte/internal/tui/ui"
)

// SafetyNumberView shows the safety number with a contact as 12 groups of 5
// digits plus a scannable QR code of the fingerprint, and toggles the
// verified state.
type SafetyNumberView struct {
	ctx     ui.Context
	aci     core.ACI
	name    string
	sn      core.SafetyNumber
	qr      *QR
	loaded  bool
	err     string
	busy    bool
	qrError string
}

// NewSafetyNumber returns the safety number overlay for a contact.
func NewSafetyNumber(ctx ui.Context, aci core.ACI) *SafetyNumberView {
	return &SafetyNumberView{ctx: ctx, aci: aci}
}

type safetyLoadedMsg struct {
	aci  core.ACI
	sn   core.SafetyNumber
	name string
	err  error
}

func (v *SafetyNumberView) load() tea.Cmd {
	svc, aci := v.ctx.Svc, v.aci
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		sn, err := svc.SafetyNumber(ctx, aci)
		name := ""
		if c, cerr := svc.Contact(ctx, aci); cerr == nil {
			name = c.DisplayName()
		}
		return safetyLoadedMsg{aci: aci, sn: sn, name: name, err: err}
	}
}

func (v *SafetyNumberView) Init() tea.Cmd      { return v.load() }
func (v *SafetyNumberView) KeyContext() string { return ContextSafety }

func (v *SafetyNumberView) Title() string {
	if v.name == "" {
		return "Safety number"
	}
	return "Safety number · " + clean(v.name)
}

func (v *SafetyNumberView) Footer() string {
	verb := "mark verified"
	if v.sn.Verified {
		verb = "clear verification"
	}
	return v.ctx.Styles.RenderKeyHints("v", verb, "esc", "close")
}

// digitsW is the width of the 3 x 4 digit block.
const digitsW = 4*5 + 3

func (v *SafetyNumberView) Size(maxW, maxH int) (int, int) {
	w, h := 52, 10
	if v.qr != nil {
		qw, qh := v.qr.CellSize(2)
		if qw+3+digitsW <= maxW {
			w = max(w, qw+3+digitsW)
			h = max(h, qh)
		} else {
			w = max(w, qw)
			h = qh + 7
		}
	}
	return min(w, maxW), min(h, maxH)
}

func (v *SafetyNumberView) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case safetyLoadedMsg:
		if msg.aci != v.aci {
			return v, nil
		}
		v.loaded = true
		if msg.name != "" {
			v.name = msg.name
		}
		if msg.err != nil {
			v.err = msg.err.Error()
			return v, nil
		}
		v.err = ""
		v.sn = msg.sn
		v.qr, v.qrError = nil, ""
		if len(msg.sn.QRData) > 0 {
			q, err := NewQR(msg.sn.QRData)
			if err != nil {
				v.qrError = err.Error()
			} else {
				v.qr = q
			}
		}
		return v, nil
	case opDoneMsg:
		v.busy = false
		return v, tea.Batch(toastFor(msg), v.load())
	case ui.EventMsg:
		if cu, ok := msg.Event.(core.ContactUpdated); ok && cu.Contact.ACI == v.aci {
			v.sn.Verified = cu.Contact.Verified
			v.name = cu.Contact.DisplayName()
		}
		if ic, ok := msg.Event.(core.IdentityChanged); ok && ic.ACI == v.aci {
			return v, v.load()
		}
		return v, nil
	}
	if k := keyOf(msg); k == "v" || isSelect(msg) {
		if !v.loaded || v.err != "" || v.busy {
			return v, nil
		}
		v.busy = true
		want := !v.sn.Verified
		aci := v.aci
		okText := "Marked as verified"
		if !want {
			okText = "Verification cleared"
		}
		return v, runOp(v.ctx.Svc, "Verify", okText, func(ctx context.Context, svc core.Service) error {
			return svc.SetVerified(ctx, aci, want)
		})
	}
	return v, nil
}

// groupDigits returns the digits as rows of four 5-digit groups.
func groupDigits(digits string) []string {
	var ds []rune
	for _, r := range digits {
		if r >= '0' && r <= '9' {
			ds = append(ds, r)
		}
	}
	var groups []string
	for i := 0; i < len(ds); i += 5 {
		groups = append(groups, string(ds[i:min(i+5, len(ds))]))
	}
	var rows []string
	for i := 0; i < len(groups); i += 4 {
		rows = append(rows, strings.Join(groups[i:min(i+4, len(groups))], " "))
	}
	return rows
}

func (v *SafetyNumberView) View(w, h int) string {
	st := v.ctx.Styles
	if !v.loaded {
		return muted(st, "Loading…")
	}
	if v.err != "" {
		lines := []string{statusLine(st, v.err, true, w)}
		return clampLines(lines, w, h)
	}
	// Right-hand (or lower) block: digits, status, explanation.
	digitStyle := lipgloss.NewStyle().Bold(true)
	if !st.Theme.Mono {
		digitStyle = digitStyle.Foreground(pal(st).Fg)
	}
	var info []string
	for _, r := range groupDigits(v.sn.Digits) {
		info = append(info, digitStyle.Render(r))
	}
	info = append(info, "")
	if v.sn.Verified {
		info = append(info, st.SuccessText.Bold(true).Render(v.ctx.Icons.Verified+" Verified"))
	} else {
		info = append(info, muted(st, "Not verified"))
	}
	name := clean(v.name)
	if name == "" {
		name = "this contact"
	}
	explain := "Compare these numbers with " + name + "'s device, or scan the code with their phone."

	var qrLines []string
	if v.qr != nil {
		side := w - digitsW - 3
		if quiet := v.qr.FitQuiet(side, h); quiet >= 1 {
			qrLines = v.qr.Render(st, quiet)
			infoW := w - width(qrLines[0]) - 3
			info = append(info, "")
			for _, l := range wrapText(explain, infoW) {
				info = append(info, muted(st, l))
			}
			return clampLines(joinColumns(qrLines, info, 3), w, h)
		}
		// Stacked: QR above the digits.
		textH := len(info) + 1
		if quiet := v.qr.FitQuiet(w, h-textH); quiet >= 1 {
			qrLines = v.qr.Render(st, quiet)
			out := make([]string, 0, h)
			pad := (w - width(qrLines[0])) / 2
			for _, l := range qrLines {
				out = append(out, spaces(pad)+l)
			}
			out = append(out, "")
			for _, l := range info {
				out = append(out, center(l, w))
			}
			return clampLines(out, w, h)
		}
	}
	// No room for the code.
	out := append([]string{}, info...)
	out = append(out, "")
	for _, l := range wrapText(explain, w) {
		out = append(out, muted(st, l))
	}
	if v.qr != nil {
		out = append(out, subtle(st, "Enlarge the window to show the QR code."))
	} else if v.qrError != "" {
		out = append(out, statusLine(st, "QR: "+v.qrError, true, w))
	}
	return clampLines(out, w, h)
}

// joinColumns places right next to left with gap spaces.
func joinColumns(left, right []string, gap int) []string {
	lw := 0
	for _, l := range left {
		lw = max(lw, width(l))
	}
	n := max(len(left), len(right))
	out := make([]string, n)
	for i := 0; i < n; i++ {
		l := ""
		if i < len(left) {
			l = left[i]
		}
		r := ""
		if i < len(right) {
			r = right[i]
		}
		out[i] = l + spaces(lw-width(l)+gap) + r
	}
	return out
}
