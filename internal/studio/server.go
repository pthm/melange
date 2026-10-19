// Package studio serves melange studio: a local, read-only web console over a
// melange-managed database. It is a presentation layer over calls the CLI
// already makes (GetDeployedModel, Checker.Explain, Checker.Expand); see
// specs/proposals/STUDIO.md.
package studio

//go:generate go tool templ generate

import (
	"cmp"
	"compress/gzip"
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"

	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/schema"
)

//go:embed assets
var assets embed.FS

// Server holds the one database connection the console talks to.
type Server struct {
	db         *sql.DB
	dbSchema   string
	env        string
	schemaPath string // optional local .fga, for the Health page's file and drift checks
	checker    *melange.Checker
	labels     *labeler

	countsMu sync.Mutex // guards the cached Tuples sidebar counts
	counts   []typeCount
	countsAt time.Time

	groupsMu sync.Mutex // guards groups, the entity access-group cache
	groups   map[string]*groupCall
}

// New returns a Server over db. env is the active environment name, shown in
// the page header; empty means the base config. schemaPath may be empty.
func New(db *sql.DB, dbSchema, env, schemaPath string) *Server {
	return &Server{
		db:         db,
		dbSchema:   dbSchema,
		env:        env,
		schemaPath: schemaPath,
		checker:    melange.NewChecker(db, melange.WithDatabaseSchema(dbSchema)),
		labels:     newLabeler(db, dbSchema),
	}
}

// Handler returns the console's routes, wrapped in a Host check.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /assets/{version}/", serveAssets())
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("POST /query", s.query)
	mux.HandleFunc("POST /explain", s.explain)
	mux.HandleFunc("GET /expand", s.expand)
	mux.HandleFunc("GET /relation", s.relation)
	mux.HandleFunc("GET /tuples", s.tuplesIndex)
	mux.HandleFunc("GET /history", s.history)
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /browse/{type}", s.browse)
	mux.HandleFunc("GET /browse/{type}/list", s.browseList)
	mux.HandleFunc("GET /entity", s.entity)
	mux.HandleFunc("GET /entity/group", s.entityGroup)
	mux.HandleFunc("GET /entity/count", s.entityCount)
	return localhostOnly(secureHeaders(gzipped(mux)))
}

// serveAssets serves the embedded assets under /assets/<version>/. A URL with
// the current assetVersion never changes content, so it is cached as
// immutable; an old version (a page from before an upgrade) revalidates.
func serveAssets() http.Handler {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err) // the embed always has an assets directory
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version := r.PathValue("version")
		if version == assetVersion {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.StripPrefix("/assets/"+version, files).ServeHTTP(w, r)
	})
}

// contentSecurityPolicy allows only studio's own scripts, styles, fonts and
// images (plus data: images, for the CSS search icon). htmx runs with
// allowEval and includeIndicatorStyles off (views.templ shell), so neither
// unsafe-eval nor unsafe-inline is needed.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// secureHeaders sets the browser hardening headers on every response. Pages
// show live authorization data, so they are never stored in a cache;
// serveAssets sets its own Cache-Control.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if !strings.HasPrefix(r.URL.Path, "/assets/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// gzipped compresses responses for clients that accept gzip. Fonts and PNGs
// are already compressed and pass through untouched.
func gzipped(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") ||
			strings.HasSuffix(r.URL.Path, ".woff2") || strings.HasSuffix(r.URL.Path, ".png") {
			next.ServeHTTP(w, r)
			return
		}
		r.Header.Del("Range") // byte ranges of the uncompressed file would be wrong
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

// gzipWriter compresses the body written through it. It holds the status
// until the first body write, so a handler that sends a status before its
// body (a 404 page) still gets its Content-Type detected from the
// uncompressed bytes; net/http would otherwise sniff the gzip stream, or
// never see a body at all. A response with no body passes through as-is,
// and Content-Length, which describes the uncompressed size, is dropped.
type gzipWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	code    int // status held until the body starts; 0 means 200
	started bool
}

func (g *gzipWriter) WriteHeader(code int) {
	if !g.started && g.code == 0 {
		g.code = code
	}
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.started {
		g.start(b)
	}
	if g.gz == nil {
		return g.ResponseWriter.Write(b)
	}
	return g.gz.Write(b)
}

// start sends the held status, compressing only when there is a body.
func (g *gzipWriter) start(b []byte) {
	g.started = true
	code := cmp.Or(g.code, http.StatusOK)
	if len(b) > 0 && code != http.StatusNoContent && code != http.StatusNotModified {
		if g.Header().Get("Content-Type") == "" {
			g.Header().Set("Content-Type", http.DetectContentType(b))
		}
		g.Header().Del("Content-Length")
		g.Header().Set("Content-Encoding", "gzip")
		g.gz = gzip.NewWriter(g.ResponseWriter)
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) close() {
	switch {
	case !g.started && g.code != 0:
		g.ResponseWriter.WriteHeader(g.code)
	case g.gz != nil:
		_ = g.gz.Close()
	}
}

// localhostOnly rejects requests whose Host is not a loopback name. The bind
// address is already loopback; this closes the DNS-rebinding hole where a
// hostile page resolves its own domain to 127.0.0.1 and reads responses.
func localhostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "localhost" && !isLoopbackIP(host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopbackIP(host string) bool {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// ReadOnlyDSN returns dsn with default_transaction_read_only=on set as a
// connection runtime parameter, so every statement the console runs is in a
// read-only transaction. Accepts URL and key=value forms.
func ReadOnlyDSN(dsn string) (string, error) {
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return dsn + " default_transaction_read_only=on", nil
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parsing database URL: %w", err)
	}
	q := u.Query()
	q.Set("default_transaction_read_only", "on")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// VerifyReadOnly fails unless the connection really is read-only, so a driver
// that ignored the runtime parameter cannot silently leave writes enabled.
func VerifyReadOnly(ctx context.Context, db *sql.DB) error {
	var v string
	if err := db.QueryRowContext(ctx, "SHOW default_transaction_read_only").Scan(&v); err != nil {
		return fmt.Errorf("checking read-only mode: %w", err)
	}
	if v != "on" {
		return fmt.Errorf("connection is not read-only (default_transaction_read_only=%s)", v)
	}
	return nil
}

type indexData struct {
	Env      string
	Model    *migrator.DeployedModel
	ModelErr string
	// Type and Relation preselect the query forms (from ?type=&relation=).
	Type     string
	Relation string
	Tab      string // modelTabQuery (default) or modelTabSchema, from ?tab=
}

const (
	modelTabQuery  = "query"
	modelTabSchema = "schema"
)

// Query form modes (the "mode" radio), in display order in queryModes.
const (
	queryExplain = "explain"
	queryExpand  = "expand"
	queryMatrix  = "matrix"
	queryAnalyze = "analyze"
)

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	data := indexData{
		Env:      s.env,
		Type:     r.URL.Query().Get("type"),
		Relation: r.URL.Query().Get("relation"),
		Tab:      modelTabQuery,
	}
	if r.URL.Query().Get("tab") == modelTabSchema {
		data.Tab = modelTabSchema
	}
	data.Model, data.ModelErr = s.loadModel(r.Context())
	render(w, r, indexPage(data))
}

// loadModel returns the deployed model, or a message saying why there is none.
func (s *Server) loadModel(ctx context.Context) (model *migrator.DeployedModel, reason string) {
	m := migrator.NewMigrator(s.db, "")
	m.SetDatabaseSchema(s.dbSchema)
	model, err := m.GetDeployedModel(ctx)
	switch {
	case err != nil:
		return nil, err.Error()
	case model == nil:
		return nil, "No model recorded. The database was never migrated, or was migrated before melange recorded deployed models; re-run `melange migrate` to record it. Explain and Expand still work."
	default:
		newModelOrder(model).sortRelations(model)
		return model, ""
	}
}

// query runs the Query form's selected mode. Only Explain uses what-if
// tuples, so the other modes never receive them.
func (s *Server) query(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		render(w, r, errorBox(err.Error()))
		return
	}
	mode := r.Form.Get("mode")
	if mode != queryExplain {
		r.Form.Del("whatif")
		r.PostForm.Del("whatif")
	}
	switch mode {
	case queryExpand:
		s.expand(w, r)
	case queryMatrix:
		s.matrix(w, r)
	case queryAnalyze:
		s.analyze(w, r)
	default:
		s.explain(w, r)
	}
}

func (s *Server) explain(w http.ResponseWriter, r *http.Request) {
	subject, err1 := parseIdent("subject", r.FormValue("subject"))
	object, err2 := parseIdent("object", r.FormValue("object"))
	relation := strings.TrimSpace(r.FormValue("relation"))
	if err := firstErr(err1, err2, required(relation, "relation")); err != nil {
		render(w, r, errorBox(err.Error()))
		return
	}
	if strings.TrimSpace(r.FormValue("whatif")) == "" {
		start := time.Now()
		trace, err := s.checker.Explain(r.Context(), subject, melange.Relation(relation), object)
		took := time.Since(start)
		if err != nil {
			render(w, r, errorBox(failedAfter(err, took)))
			return
		}
		render(w, r, traceView(trace, took))
		return
	}
	s.explainWithWhatIf(w, r, subject, melange.Relation(relation), object)
}

// explainWithWhatIf runs the query without and with the form's what-if
// tuples. The tuples are parsed first, so a typo costs no Explain.
func (s *Server) explainWithWhatIf(w http.ResponseWriter, r *http.Request, subject melange.Object, relation melange.Relation, object melange.Object) {
	model, unchecked := s.loadModel(r.Context())
	var types []schema.TypeDefinition
	if model != nil {
		types = model.Types
	}
	tuples, err := parseWhatIf(r.FormValue("whatif"), types)
	if err != nil {
		render(w, r, errorBox("What-if tuples:\n"+err.Error()))
		return
	}
	start := time.Now()
	base, err := s.checker.Explain(r.Context(), subject, relation, object)
	baseTook := time.Since(start)
	if err != nil {
		render(w, r, errorBox("Without what-if: "+failedAfter(err, baseTook)))
		return
	}
	start = time.Now()
	with, err := s.explainWhatIf(r.Context(), tuples, subject, relation, object)
	withTook := time.Since(start)
	if err != nil {
		render(w, r, errorBox("With what-if: "+failedAfter(err, withTook)))
		return
	}
	render(w, r, whatIfView(base, with, tuples, unchecked, baseTook, withTook))
}

// matrixCell is one relation's answer in the check matrix.
type matrixCell struct {
	Relation string
	Allowed  bool
	Err      string
}

// matrix checks every relation on the object's type for the subject. The
// relations come from the deployed model, since check_permission answers
// false (not an error) for a relation that doesn't exist.
func (s *Server) matrix(w http.ResponseWriter, r *http.Request) {
	subject, err1 := parseIdent("subject", r.FormValue("subject"))
	object, err2 := parseIdent("object", r.FormValue("object"))
	if err := firstErr(err1, err2); err != nil {
		render(w, r, errorBox(err.Error()))
		return
	}
	model, reason := s.loadModel(r.Context())
	if model == nil {
		render(w, r, errorBox(reason))
		return
	}
	if !typeExists(model.Types, string(object.Type)) {
		render(w, r, errorBox(fmt.Sprintf("%s is not a type in the deployed model", object.Type)))
		return
	}
	var notes []string
	if !typeExists(model.Types, string(subject.Type)) {
		notes = append(notes, fmt.Sprintf("%s is not a type in the deployed model, so every check is denied.", subject.Type))
	}
	rels := relationsOf(model.Types, string(object.Type))
	if len(rels) == 0 {
		render(w, r, errorBox(fmt.Sprintf("%s has no relations in the deployed model", object.Type)))
		return
	}
	cells := make([]matrixCell, len(rels))
	start := time.Now()
	errs := eachRelation(r.Context(), rels, func(ctx context.Context, i int, rel string) (err error) {
		cells[i].Allowed, err = s.checker.Check(ctx, subject, melange.Relation(rel), object)
		return err
	})
	took := time.Since(start)
	for i, rel := range rels {
		cells[i].Relation, cells[i].Err = rel, errString(errs[i])
	}
	render(w, r, matrixView(string(subject.Type)+":"+subject.ID, string(object.Type)+":"+object.ID, cells, notes, took))
}

// expand serves both the form (object + relation) and pointer drill-in
// (userset = "type:id#relation", as Leaf.Computed carries it).
func (s *Server) expand(w http.ResponseWriter, r *http.Request) {
	rawObject, relation := r.FormValue("object"), strings.TrimSpace(r.FormValue("relation"))
	if us := r.FormValue("userset"); us != "" {
		var ok bool
		if rawObject, relation, ok = strings.Cut(us, "#"); !ok {
			render(w, r, errorBox(fmt.Sprintf("expected <type>:<id>#<relation>, got %q", us)))
			return
		}
	}
	object, err := parseIdent("object", rawObject)
	if err := firstErr(err, required(relation, "relation")); err != nil {
		render(w, r, errorBox(err.Error()))
		return
	}
	start := time.Now()
	tree, err := s.checker.Expand(r.Context(), object, melange.Relation(relation))
	took := time.Since(start)
	if err != nil {
		render(w, r, errorBox(failedAfter(err, took)))
		return
	}
	render(w, r, expandView(tree, r.FormValue("userset") == "", took))
}

func render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	if err := c.Render(r.Context(), w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// parseIdent parses "type:id"; field names the input in the error.
func parseIdent(field, raw string) (melange.Object, error) {
	typ, id, ok := strings.Cut(strings.TrimSpace(raw), ":")
	if !ok || typ == "" || id == "" {
		return melange.Object{}, fmt.Errorf("%s: expected <type>:<id>, got %q", field, raw)
	}
	return melange.Object{Type: melange.ObjectType(typ), ID: id}, nil
}

func required(v, name string) error {
	if v == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
