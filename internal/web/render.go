package web

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Renderer parses and executes html/templates.
type Renderer struct {
	fs      fs.FS
	funcMap template.FuncMap
}

// NewRenderer creates a new template renderer backed by an fs.FS.
func NewRenderer(templateFS fs.FS) *Renderer {
	r := &Renderer{
		fs: templateFS,
	}

	r.funcMap = template.FuncMap{
		"formatDate": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.Format("2006-01-02")
		},
		"formatDisplayDate": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.Format("Jan 02, 2006")
		},
		// Payment method names come from the database now, so there is nothing
		// to map: this only turns "no method" -- an opening balance -- into
		// something readable.
		"paymentMethodLabel": func(name *string) string {
			if name == nil || *name == "" {
				return "-"
			}
			return *name
		},
		"isBank": func(t AccountType) bool {
			return t == AccountTypeBank
		},
		"derefString": func(s *string) string {
			if s == nil {
				return ""
			}
			return *s
		},
		"derefSubtype": func(s *AccountSubtype) string {
			if s == nil {
				return ""
			}
			return string(*s)
		},
		"isNegative": func(n int64) bool {
			return n < 0
		},
		"formatAmount": func(val any) string {
			switch v := val.(type) {
			case string:
				return formatAmountString(v)
			case int64:
				return formatAmountString(strconv.FormatInt(v, 10))
			case int:
				return formatAmountString(strconv.Itoa(v))
			default:
				return formatAmountString(fmt.Sprintf("%v", val))
			}
		},
		"formatSignedAmount": func(tx TransactionView) string {
			amt := formatAmountString(tx.AmountFormatted)
			if tx.Type == TransactionTypeIncome && !strings.HasPrefix(amt, "+") {
				return "+" + amt
			}
			return amt
		},
		"lower": strings.ToLower,
		"upper": strings.ToUpper,
		"seq": func(a, b any) bool {
			return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
		},
		"formatDateTime": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.Format("2006-01-02 15:04:05 UTC")
		},
		"formatBytes": func(b int64) string {
			const unit = 1024
			if b < unit {
				return fmt.Sprintf("%d B", b)
			}
			div, exp := int64(unit), 0
			for n := b / unit; n >= unit; n /= unit {
				div *= unit
				exp++
			}
			return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
		},
	}

	return r
}

// IsHXRequest checks if the incoming request has the HX-Request header
func IsHXRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// RenderPage renders a full HTML page with the base layout.
func (rn *Renderer) RenderPage(w http.ResponseWriter, r *http.Request, status int, pageTemplate string, data PageData) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	cleanPage := filepath.ToSlash(pageTemplate)
	cleanPage = strings.TrimPrefix(cleanPage, "/")

	tmpl, err := template.New("base.html").Funcs(rn.funcMap).ParseFS(
		rn.fs,
		"layouts/base.html",
		"components/nav.html",
		"components/alerts.html",
		"components/empty_state.html",
		cleanPage,
	)
	if err != nil {
		http.Error(w, fmt.Sprintf("Template parse error: %v", err), http.StatusInternalServerError)
		return err
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "base.html", data); err != nil {
		http.Error(w, fmt.Sprintf("Template execute error: %v", err), http.StatusInternalServerError)
		return err
	}

	w.WriteHeader(status)
	_, err = io.Copy(w, &buf)
	return err
}

// RenderPartial renders only a partial template fragment without the full layout.
func (rn *Renderer) RenderPartial(w http.ResponseWriter, r *http.Request, status int, partialTemplate string, templateName string, data any) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	cleanPartial := filepath.ToSlash(partialTemplate)
	cleanPartial = strings.TrimPrefix(cleanPartial, "/")

	content, err := fs.ReadFile(rn.fs, cleanPartial)
	if err != nil {
		http.Error(w, fmt.Sprintf("Partial read error: %v", err), http.StatusInternalServerError)
		return err
	}

	tmpl, err := template.New(templateName).Funcs(rn.funcMap).Parse(string(content))
	if err != nil {
		http.Error(w, fmt.Sprintf("Partial parse error: %v", err), http.StatusInternalServerError)
		return err
	}

	var buf bytes.Buffer
	if t := tmpl.Lookup(templateName); t != nil {
		err = tmpl.ExecuteTemplate(&buf, templateName, data)
	} else {
		err = tmpl.Execute(&buf, data)
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("Partial execute error: %v", err), http.StatusInternalServerError)
		return err
	}

	w.WriteHeader(status)
	_, err = io.Copy(w, &buf)
	return err
}

// formatAmountString formats a decimal amount with thousands separators, preserving decimal places.
// E.g. "1234567.89" -> "1,234,567.89", "-25.00" -> "-25.00", "0.00" -> "0.00".
func formatAmountString(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	sign := ""
	if strings.HasPrefix(s, "-") {
		sign = "-"
		s = s[1:]
	} else if strings.HasPrefix(s, "+") {
		sign = "+"
		s = s[1:]
	}

	intPart, fracPart, hasPoint := strings.Cut(s, ".")

	var buf strings.Builder
	n := len(intPart)
	for i, ch := range intPart {
		if i > 0 && (n-i)%3 == 0 {
			buf.WriteByte(',')
		}
		buf.WriteRune(ch)
	}

	if hasPoint {
		return sign + buf.String() + "." + fracPart
	}
	return sign + buf.String()
}
