// Command codemail receives email for your domains, extracts sign-in codes
// and links, and delivers them to your devices.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/mail"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/tikaydaes/codemail/internal/api"
	"github.com/tikaydaes/codemail/internal/extract"
	"github.com/tikaydaes/codemail/internal/hub"
	"github.com/tikaydaes/codemail/internal/mailauth"
	"github.com/tikaydaes/codemail/internal/mailmsg"
	"github.com/tikaydaes/codemail/internal/smtpd"
	"github.com/tikaydaes/codemail/internal/store"
	"github.com/tikaydaes/codemail/internal/store/sqlstore"
	"github.com/tikaydaes/codemail/internal/web"
)

const usage = `usage: codemail <command> [flags]

  serve                         run the server
  domain add [flags] <domain>   add a shared domain from the command line
  recovery <username>           issue a one-time recovery code for a user
  backup <file>                 write a consistent copy of the SQLite database
  migrate-db -from A -to B      copy everything into another (empty) database
  extract <file|dir>...         run the extractor over .eml or mbox files
  markdown <file|dir>...        print messages as the Markdown that clients display
  train <dir>                   fit the extractor's weights on labelled mail

serve, domain, recovery and backup take -data <dir> (default ./data) and
-db <url>. The database is SQLite in the data directory unless -db (or the
CODEMAIL_DB environment variable) gives a PostgreSQL URL such as
postgres://user:password@host/codemail.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "serve":
		err = serve(args)
	case "domain":
		if len(args) == 0 || args[0] != "add" {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		err = addDomain(args[1:])
	case "recovery":
		err = recovery(args)
	case "backup":
		err = backup(args)
	case "migrate-db":
		err = migrateDB(args)
	case "extract":
		err = extractFiles(args)
	case "markdown":
		err = markdownFiles(args)
	case "train":
		err = train(args)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "codemail:", err)
		os.Exit(1)
	}
}

// dbFlag adds the -db flag, defaulting to the CODEMAIL_DB environment variable.
func dbFlag(fl *flag.FlagSet) *string {
	return fl.String("db", os.Getenv("CODEMAIL_DB"), "PostgreSQL URL; empty uses SQLite in the data directory")
}

// openDB opens a database named either by a PostgreSQL URL or by the path
// of a SQLite file.
func openDB(target string) (*sqlstore.DB, error) {
	if strings.HasPrefix(target, "postgres://") || strings.HasPrefix(target, "postgresql://") {
		return sqlstore.OpenPostgres(target)
	}
	return sqlstore.Open(target)
}

func openStore(dir, dbURL string) (*sqlstore.DB, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if dbURL != "" {
		return openDB(dbURL)
	}
	return sqlstore.Open(filepath.Join(dir, "codemail.db"))
}

// backup writes a snapshot of the SQLite database that is safe to take
// while the server is running.
func backup(args []string) error {
	fl := flag.NewFlagSet("backup", flag.ExitOnError)
	data := fl.String("data", "./data", "data directory")
	dbURL := dbFlag(fl)
	fl.Parse(args)
	if fl.NArg() != 1 {
		return fmt.Errorf("backup takes the file to write")
	}
	st, err := openStore(*data, *dbURL)
	if err != nil {
		return err
	}
	defer st.Close()
	return st.Backup(context.Background(), fl.Arg(0))
}

// migrateDB copies a whole database into another, empty one: SQLite to
// PostgreSQL or back. Stop the server first.
func migrateDB(args []string) error {
	fl := flag.NewFlagSet("migrate-db", flag.ExitOnError)
	from := fl.String("from", "", "source: a SQLite file or a PostgreSQL URL")
	to := fl.String("to", "", "destination: a SQLite file or a PostgreSQL URL; must be empty")
	fl.Parse(args)
	if *from == "" || *to == "" {
		return fmt.Errorf("migrate-db needs -from and -to")
	}
	if !strings.Contains(*from, "://") {
		if _, err := os.Stat(*from); err != nil {
			return fmt.Errorf("source: %w", err)
		}
	}
	src, err := openDB(*from)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	defer src.Close()
	dst, err := openDB(*to)
	if err != nil {
		return fmt.Errorf("destination: %w", err)
	}
	defer dst.Close()
	if err := sqlstore.Copy(context.Background(), src, dst); err != nil {
		return err
	}
	stats, err := dst.Stats(context.Background())
	if err != nil {
		return err
	}
	fmt.Printf("copied %d users and %d messages\n", stats.Users, stats.Messages)
	return nil
}

func serve(args []string) error {
	fl := flag.NewFlagSet("serve", flag.ExitOnError)
	data := fl.String("data", "./data", "data directory")
	dbURL := dbFlag(fl)
	hostname := fl.String("hostname", "", "public hostname of the web app, e.g. mail.example.com")
	mailHost := fl.String("mail-host", "", "hostname that MX records point at, if different from -hostname")
	smtpCert := fl.String("smtp-cert", "", "certificate file for SMTP STARTTLS with -behind-proxy (default: a self-signed one)")
	smtpKey := fl.String("smtp-key", "", "key file for -smtp-cert")
	smtpAddr := fl.String("smtp", ":25", "SMTP listen address")
	httpAddr := fl.String("http", ":8080", "HTTP listen address, used with -dev or -behind-proxy")
	behindProxy := fl.Bool("behind-proxy", false, "serve plain HTTP on -http; a proxy in front provides HTTPS")
	dev := fl.Bool("dev", false, "local development: http://localhost:8080, SMTP on :2525, no certificates or sender checks")
	trustSenders := fl.Bool("dev-trust-senders", false, "with -dev: treat every sender as verified, for testing the browser extension")
	retention := fl.Duration("retention", 7*24*time.Hour, "how long to keep messages by default")
	maxBytes := fl.Int64("max-bytes", 10<<20, "largest accepted message")
	fl.Parse(args)

	origin := "https://" + *hostname
	if *dev {
		*hostname, origin = "localhost", "http://localhost"+*httpAddr
		if *smtpAddr == ":25" {
			*smtpAddr = ":2525"
		}
	} else if *hostname == "" {
		return fmt.Errorf("serve needs -hostname (or -dev for local use)")
	}
	if *mailHost == "" {
		*mailHost = *hostname
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	st, err := openStore(*data, *dbURL)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	h := hub.New()
	apiSrv, err := api.New(api.Config{Origin: origin, MailHost: *mailHost, DefaultRetention: *retention, TrustProxy: *behindProxy},
		st, h, extract.Default, web.Files(), log)
	if err != nil {
		return err
	}
	if token, err := apiSrv.EnsureSetupToken(ctx); err != nil {
		return err
	} else if token != "" {
		log.Info("no accounts yet: open this link to create the admin account", "url", origin+"/#/setup/"+token)
	}

	backend := &smtpd.Backend{
		Store: st, Hub: h, Model: extract.Default, Log: log,
		Cfg: smtpd.Config{Hostname: *mailHost, Retention: *retention, MaxBytes: *maxBytes, PerIPRate: 60, DailyLimit: 2000},
	}
	httpSrv := &http.Server{Addr: *httpAddr, Handler: apiSrv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 3)

	switch {
	case *dev:
		if *trustSenders {
			backend.Auth = mailauth.TrustAll{}
		}
		go func() { errc <- httpSrv.ListenAndServe() }()
	case *behindProxy:
		// The proxy holds the web certificate, so SMTP needs its own.
		// Mail servers encrypt opportunistically and do not check the
		// certificate, so a self-signed one still protects mail in transit.
		cert, err := smtpCertificate(*smtpCert, *smtpKey, *data, *mailHost)
		if err != nil {
			return err
		}
		backend.Auth = mailauth.DNS{}
		backend.Cfg.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		go func() { errc <- httpSrv.ListenAndServe() }()
	default:
		// Certificates for the hostname come from Let's Encrypt and are
		// shared by HTTPS and SMTP STARTTLS.
		certs := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(*hostname, *mailHost),
			Cache:      autocert.DirCache(filepath.Join(*data, "certs")),
		}
		backend.Auth = mailauth.DNS{}
		backend.Cfg.TLS = &tls.Config{GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if hello.ServerName == "" {
				hello.ServerName = *mailHost // SMTP clients often send no name
			}
			return certs.GetCertificate(hello)
		}}
		httpSrv.Addr, httpSrv.TLSConfig = ":443", certs.TLSConfig()
		go func() { errc <- httpSrv.ListenAndServeTLS("", "") }()
		go func() { errc <- http.ListenAndServe(":80", certs.HTTPHandler(nil)) }()
	}

	smtpSrv := smtpd.NewServer(backend)
	smtpSrv.Addr = *smtpAddr
	go func() { errc <- smtpSrv.ListenAndServe() }()
	log.Info("database", "kind", map[bool]string{true: "postgresql", false: "sqlite"}[st.Postgres()])
	log.Info("listening", "smtp", *smtpAddr, "http", httpSrv.Addr, "origin", origin, "mail_host", *mailHost)

	go func() {
		hourly := time.NewTicker(time.Hour)
		defer hourly.Stop()
		for tick := 0; ; tick++ {
			if n, err := st.DeleteExpired(ctx, time.Now()); err != nil {
				log.Error("delete expired", "err", err)
			} else if n > 0 {
				log.Info("deleted expired messages", "count", n)
			}
			if tick%24 == 0 && !*dev {
				apiSrv.RecheckDomains(ctx)
			}
			select {
			case <-ctx.Done():
				return
			case <-hourly.C:
			}
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		smtpSrv.Close()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdown)
		return nil
	}
}

// addDomain adds a shared domain without going through the web app.
func addDomain(args []string) error {
	fl := flag.NewFlagSet("domain add", flag.ExitOnError)
	data := fl.String("data", "./data", "data directory")
	dbURL := dbFlag(fl)
	mailbox := fl.Bool("mailbox", true, "allow username@domain")
	aliases := fl.Bool("aliases", true, "allow claimed and generated aliases")
	subdomains := fl.Bool("subdomains", true, "allow anything@username.domain")
	maxAliases := fl.Int("max-aliases", 20, "claimed aliases per user, 0 for unlimited")
	fl.Parse(args)
	if fl.NArg() != 1 {
		return fmt.Errorf("domain add takes one domain")
	}
	st, err := openStore(*data, *dbURL)
	if err != nil {
		return err
	}
	defer st.Close()
	return st.AddDomain(context.Background(), &store.Domain{Name: fl.Arg(0), Verified: true,
		AllowMailbox: *mailbox, AllowAliases: *aliases, AllowSubdomains: *subdomains, MaxAliases: *maxAliases})
}

// recovery prints a new one-time recovery code, for an admin who is
// locked out and has shell access to the server.
func recovery(args []string) error {
	fl := flag.NewFlagSet("recovery", flag.ExitOnError)
	data := fl.String("data", "./data", "data directory")
	dbURL := dbFlag(fl)
	fl.Parse(args)
	if fl.NArg() != 1 {
		return fmt.Errorf("recovery takes one username")
	}
	st, err := openStore(*data, *dbURL)
	if err != nil {
		return err
	}
	defer st.Close()
	code, err := api.IssueRecoveryCode(context.Background(), st, fl.Arg(0))
	if err != nil {
		return err
	}
	fmt.Println(code)
	return nil
}

// eachMessage parses every message under the given .eml files, mbox files
// or directories in parallel and returns work's results in a stable order.
// File names are reported relative to the root they were found under.
func eachMessage[T any](roots []string, work func(file string, index int, in extract.Input, perr error) T) ([]T, error) {
	type item struct{ path, rel string }
	var items []item
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasSuffix(path, ".jsonl") {
				return err // label files live beside the mail
			}
			rel, _ := filepath.Rel(root, path)
			items = append(items, item{path, rel})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	results := make([][]T, len(items))
	errs := make([]error, len(items))
	var wg sync.WaitGroup
	next := make(chan int)
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				raw, err := os.ReadFile(items[i].path)
				if err != nil {
					errs[i] = err
					continue
				}
				for j, msg := range splitMbox(raw) {
					in, perr := mailmsg.Parse(msg)
					results[i] = append(results[i], work(items[i].rel, j, in, perr))
				}
			}
		}()
	}
	for i := range items {
		next <- i
	}
	close(next)
	wg.Wait()
	var out []T
	for i, r := range results {
		if errs[i] != nil {
			return nil, errs[i]
		}
		out = append(out, r...)
	}
	return out, nil
}

// extractFiles prints one JSON line per message.
func extractFiles(args []string) error {
	fl := flag.NewFlagSet("extract", flag.ExitOnError)
	top := fl.Int("candidates", 0, "include the best N scored candidates of each kind")
	fl.Parse(args)
	lines, err := eachMessage(fl.Args(), func(file string, index int, in extract.Input, perr error) map[string]any {
		res := extract.Default.Extract(in)
		var keep []extract.Candidate
		n := map[string]int{}
		for _, c := range res.Candidates {
			if n[c.Kind] < *top {
				n[c.Kind]++
				keep = append(keep, c)
			}
		}
		res.Candidates = keep
		out := map[string]any{"file": file, "index": index, "from": in.From,
			"subject": in.Subject, "bulk": in.Bulk, "result": res}
		if perr != nil {
			out["parse_error"] = perr.Error()
		}
		return out
	})
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			return err
		}
	}
	return nil
}

// loadCorrections reads the admin page's export: one JSON object per
// correction, each carrying the message it applies to. A correction says
// what the code or the link really is (or that there is none); the other
// kind is left unknown unless it was corrected too.
func loadCorrections(path string) ([]extract.Prepared, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	type correction struct {
		MessageID int64 `json:"message_id"`
		Kind      string
		Value     string
		Raw       []byte // base64 in the file
	}
	examples := map[int64]*extract.Example{}
	var order []int64
	dec := json.NewDecoder(f)
	for dec.More() {
		var c correction
		if err := dec.Decode(&c); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		ex := examples[c.MessageID]
		if ex == nil {
			in, _ := mailmsg.Parse(c.Raw)
			group := in.From
			if a, err := mail.ParseAddress(in.From); err == nil {
				group = a.Address[strings.LastIndexByte(a.Address, '@')+1:]
			}
			ex = &extract.Example{In: in, Code: extract.Unknown, Link: extract.Unknown, Group: strings.ToLower(group)}
			examples[c.MessageID] = ex
			order = append(order, c.MessageID)
		}
		// Later corrections of the same message replace earlier ones.
		switch c.Kind {
		case "code":
			ex.Code = c.Value
		case "link":
			ex.Link = c.Value
		}
	}
	out := make([]extract.Prepared, 0, len(order))
	for _, id := range order {
		out = append(out, extract.Prepare(*examples[id]))
	}
	return out, nil
}

// markdownFiles prints each message's Markdown rendering.
func markdownFiles(args []string) error {
	bodies, err := eachMessage(args, func(file string, _ int, in extract.Input, _ error) string {
		return "===== " + file + " | " + in.Subject + "\n" + mailmsg.Markdown(in) + "\n"
	})
	for _, b := range bodies {
		fmt.Println(b)
	}
	return err
}

// train fits the extractor's weights on labelled mail. The labels file has
// one JSON object per message that contains a code or link:
//
//	{"file": "inbox/Inbox/1.eml", "code": "123456"}
//	{"file": "inbox/Inbox/2.eml", "link": "https://..."}
//
// Messages under the root that are not listed count as having neither. A
// value of "?" leaves the message out for that kind.
func train(args []string) error {
	fl := flag.NewFlagSet("train", flag.ExitOnError)
	labelsPath := fl.String("labels", "corpus/labels.jsonl", "labels file")
	out := fl.String("out", "internal/extract/weights.json", "where to write the trained weights")
	correctionsPath := fl.String("corrections", "", "corrections exported from the admin page, to learn from as well")
	evalOnly := fl.Bool("eval", false, "only measure the built-in model")
	cv := fl.Bool("cv", true, "also estimate accuracy on unseen senders (slow)")
	fl.Parse(args)
	if fl.NArg() != 1 {
		return fmt.Errorf("train takes one mail directory")
	}

	type label struct{ File, Code, Link string }
	labels := map[string]label{}
	f, err := os.Open(*labelsPath)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(f)
	for dec.More() {
		var l label
		if err := dec.Decode(&l); err != nil {
			f.Close()
			return fmt.Errorf("%s: %w", *labelsPath, err)
		}
		labels[l.File] = l
	}
	f.Close()

	used := map[string]bool{}
	var mu sync.Mutex
	data, err := eachMessage(fl.Args(), func(file string, _ int, in extract.Input, _ error) extract.Prepared {
		l := labels[file]
		if l.File != "" {
			mu.Lock()
			used[file] = true
			mu.Unlock()
		}
		group := in.From
		if a, err := mail.ParseAddress(in.From); err == nil {
			group = a.Address[strings.LastIndexByte(a.Address, '@')+1:]
		}
		return extract.Prepare(extract.Example{In: in, Code: l.Code, Link: l.Link, Group: strings.ToLower(group)})
	})
	if err != nil {
		return err
	}
	if len(used) != len(labels) {
		return fmt.Errorf("%d labels name files that are not under %s", len(labels)-len(used), fl.Arg(0))
	}

	if *correctionsPath != "" {
		corrected, err := loadCorrections(*correctionsPath)
		if err != nil {
			return err
		}
		fmt.Printf("%d corrected messages from %s\n", len(corrected), *correctionsPath)
		data = append(data, corrected...)
	}

	report := func(name string, c, l extract.Metrics, cp, lp int) {
		fmt.Printf("%-28s code: precision %.3f recall %.3f (%d right, %d wrong, %d missed of %d)\n",
			name, c.Precision(), c.Recall(cp), c.Correct, c.Wrong, c.Missed, cp)
		fmt.Printf("%-28s link: precision %.3f recall %.3f (%d right, %d wrong, %d missed of %d)\n",
			"", l.Precision(), l.Recall(lp), l.Correct, l.Wrong, l.Missed, lp)
	}
	fmt.Printf("%d messages, %d labelled\n", len(data), len(labels))
	c, l, cp, lp := extract.Evaluate(data, extract.Default)
	report("built-in model", c, l, cp, lp)
	if *evalOnly {
		return nil
	}
	if *cv {
		c, l, cp, lp = extract.CrossValidate(data)
		report("unseen senders (5-fold)", c, l, cp, lp)
	}
	model := extract.Train(data)
	c, l, cp, lp = extract.Evaluate(data, model)
	report("trained, on training mail", c, l, cp, lp)

	b, err := json.MarshalIndent(model, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", *out, "- rebuild to use it")
	return nil
}

// splitMbox splits an mbox file into messages. Any other file is returned
// as a single message.
func splitMbox(raw []byte) [][]byte {
	if !bytes.HasPrefix(raw, []byte("From ")) {
		return [][]byte{raw}
	}
	var out [][]byte
	var cur []byte
	for _, line := range bytes.SplitAfter(raw, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("From ")) {
			if len(cur) > 0 {
				out = append(out, cur)
			}
			cur = nil
			continue
		}
		if strings.HasPrefix(string(line), ">From ") {
			line = line[1:]
		}
		cur = append(cur, line...)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}
