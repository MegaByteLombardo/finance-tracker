package web

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
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
		"paymentMethodLabel": func(m any) string {
			switch v := m.(type) {
			case PaymentMethod:
				return PaymentMethodLabel(v)
			case *PaymentMethod:
				if v != nil {
					return PaymentMethodLabel(*v)
				}
				return "-"
			case string:
				return PaymentMethodLabel(PaymentMethod(v))
			default:
				return "-"
			}
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
		"formatSignedAmount": func(tx TransactionView) string {
			if tx.Type == TransactionTypeIncome {
				return "+" + tx.AmountFormatted
			}
			return tx.AmountFormatted
		},
		"lower": strings.ToLower,
		"upper": strings.ToUpper,
		"seq": func(a, b any) bool {
			return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
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
