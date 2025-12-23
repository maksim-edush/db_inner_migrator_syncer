package httpserver

import (
	"bytes"
	"html/template"
	"net/http"
	"time"

	"db_inner_migrator_syncer/internal/auth"
	"db_inner_migrator_syncer/internal/store"
	"db_inner_migrator_syncer/web"
)

type UIData struct {
	Title         string
	Template      string
	User          *auth.User
	Projects      []store.Project
	ActiveProject *store.Project
	CSRFToken     string
	Flash         *auth.FlashMessage
	Path          string
	Page          any
}

type TemplateRenderer struct {
	tmpl *template.Template
}

func NewTemplateRenderer() *TemplateRenderer {
	var tmpl *template.Template
	funcs := template.FuncMap{
		"formatTime": func(t time.Time) string {
			return t.Format("2006-01-02 15:04")
		},
		"formatMaybeTime": func(t *time.Time) string {
			if t == nil {
				return "-"
			}
			return t.Format("2006-01-02 15:04")
		},
		"formatDate": func(t time.Time) string {
			return t.Format("2006-01-02")
		},
		"eq": func(a, b any) bool { return a == b },
		"hasRole": func(user *auth.User, role string) bool {
			if user == nil {
				return false
			}
			return string(user.Role) == role
		},
		"include": func(name string, data any) (template.HTML, error) {
			var buf bytes.Buffer
			if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
				return "", err
			}
			return template.HTML(buf.String()), nil
		},
	}

	tmpl = template.Must(template.New("base").Funcs(funcs).ParseFS(web.TemplatesFS(), "*.tmpl", "partials/*.tmpl"))
	return &TemplateRenderer{tmpl: tmpl}
}

func (r *TemplateRenderer) Render(w http.ResponseWriter, data UIData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := r.tmpl.ExecuteTemplate(w, "base", data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}
